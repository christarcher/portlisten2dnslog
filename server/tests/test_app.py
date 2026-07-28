from __future__ import annotations

import os
import tempfile
import threading
import unittest
import urllib.parse
from dataclasses import replace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

from portlistener2dns_server.app import (
    Collector,
    DNSLogClient,
    DNSLogRecord,
    EventStore,
    PendingEvent,
    Settings,
    TelegramClient,
    format_message,
    load_settings_from_env,
)
from portlistener2dns_server.protocol import Event, build_qname


class FakeDNSLog:
    def __init__(self, records: list[DNSLogRecord]) -> None:
        self.records = records

    def fetch(self) -> list[DNSLogRecord]:
        return self.records


class FakeTelegram:
    def __init__(self, *, fail: bool = False) -> None:
        self.fail = fail
        self.sent: list[PendingEvent] = []

    def send(self, pending: PendingEvent) -> None:
        if self.fail:
            raise RuntimeError("temporary Telegram failure")
        self.sent.append(pending)


class AppTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.settings = Settings(
            dnslog_token="dns-token",
            base_domain="abc.dnslog.test",
            xor_key=b"test-key",
            auth_key=b"authentication-test-key-32-bytes!",
            database_path=Path(self.temporary.name) / "events.db",
            telegram_token="bot-token",
            telegram_chat_id="123",
        )
        self.event = Event(
            event_id="37604be5-aa6f-439b-8c09-cd06efb5b23b",
            source_ip="192.0.2.10",
            source_port=54321,
            target_ip="10.0.0.8",
            target_port=445,
            timestamp=1_722_470_400,
        )
        self.qname = build_qname(
            self.event,
            base_domain=self.settings.base_domain,
            marker=self.settings.marker,
            xor_key=self.settings.xor_key,
            auth_key=self.settings.auth_key,
        )
        self.record = DNSLogRecord(
            subdomain=self.qname,
            observed_at="2026-07-27 13:42:25",
            resolver_address="101.206.203.9:57703",
        )

    def test_collector_persistently_deduplicates_uuid(self) -> None:
        store = EventStore(self.settings.database_path)
        self.addCleanup(store.close)
        telegram = FakeTelegram()
        collector = Collector(
            self.settings,
            store,
            FakeDNSLog([self.record, self.record]),
            telegram,
        )

        self.assertTrue(collector.cycle())
        self.assertEqual(len(telegram.sent), 1)
        self.assertEqual(store.status(self.event.event_id), "sent")

        self.assertTrue(collector.cycle())
        self.assertEqual(len(telegram.sent), 1)

    def test_settings_are_loaded_only_from_env_and_then_unset(self) -> None:
        environment = {
            "P2D_DNSLOG_TOKEN": "dns-token",
            "P2D_DOMAIN": "abc.dnslog.test",
            "P2D_XOR_KEY": "test-key",
            "P2D_AUTH_KEY": "authentication-test-key-32-bytes!",
            "P2D_TELEGRAM_BOT_TOKEN": "bot-token",
            "P2D_TELEGRAM_CHAT_ID": "123",
            "P2D_DB_PATH": "state/events.db",
            "P2D_RUN_ONCE": "true",
            "P2D_CHECK_CONFIG": "false",
            "P2D_VERBOSE": "true",
        }
        with patch.dict(os.environ, environment, clear=True):
            settings, run_once, check_config, verbose = load_settings_from_env()
            self.assertEqual(settings.base_domain, "abc.dnslog.test")
            self.assertEqual(settings.auth_key, environment["P2D_AUTH_KEY"].encode())
            self.assertTrue(settings.database_path.is_absolute())
            self.assertTrue(run_once)
            self.assertFalse(check_config)
            self.assertTrue(verbose)
            for name in environment:
                self.assertNotIn(name, os.environ)

    def test_failed_notification_stays_pending(self) -> None:
        store = EventStore(self.settings.database_path)
        self.addCleanup(store.close)
        telegram = FakeTelegram(fail=True)
        collector = Collector(
            self.settings,
            store,
            FakeDNSLog([self.record]),
            telegram,
        )

        self.assertFalse(collector.cycle())
        self.assertEqual(store.status(self.event.event_id), "pending")

        telegram.fail = False
        self.assertTrue(collector.cycle())
        self.assertEqual(store.status(self.event.event_id), "sent")
        self.assertEqual(len(telegram.sent), 1)

    def test_message_contains_five_tuple_and_uuid(self) -> None:
        message = format_message(
            PendingEvent(
                event=self.event,
                observed_at=self.record.observed_at,
                resolver_address=self.record.resolver_address,
                attempts=0,
            )
        )
        self.assertIn("192.0.2.10:54321", message)
        self.assertIn("10.0.0.8:445", message)
        self.assertIn(self.event.event_id, message)
        self.assertIn("2024-08-01T00:00:00+00:00", message)

    def test_real_http_clients_parse_dnslog_and_post_telegram(self) -> None:
        qname = self.qname

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802 - HTTP handler API
                self.server.get_path = self.path
                body = (
                    '{"0":{"subdomain":"'
                    + qname
                    + '.","time":"2026-07-27 13:42:25","ip":"127.0.0.1:53"}}'
                ).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self) -> None:  # noqa: N802 - HTTP handler API
                size = int(self.headers["Content-Length"])
                self.server.post_path = self.path
                self.server.post_body = self.rfile.read(size)
                body = b'{"ok":true,"result":{"message_id":1}}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format: str, *args: object) -> None:
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join, 2)
        self.addCleanup(server.shutdown)

        base_url = f"http://{server.server_address[0]}:{server.server_address[1]}"
        settings = replace(
            self.settings,
            dnslog_url=base_url + "/dns/{token}",
            telegram_api_base=base_url,
        )
        records = DNSLogClient(settings).fetch()
        self.assertEqual(len(records), 1)
        self.assertEqual(records[0].subdomain, self.qname + ".")
        self.assertEqual(records[0].observed_at, self.record.observed_at)
        self.assertEqual(records[0].resolver_address, "127.0.0.1:53")
        self.assertEqual(server.get_path, "/dns/dns-token")

        TelegramClient(settings).send(
            PendingEvent(
                event=self.event,
                observed_at=self.record.observed_at,
                resolver_address=self.record.resolver_address,
                attempts=0,
            )
        )
        self.assertEqual(server.post_path, "/botbot-token/sendMessage")
        form = urllib.parse.parse_qs(server.post_body.decode())
        self.assertEqual(form["chat_id"], ["123"])
        self.assertIn(self.event.event_id, form["text"][0])


if __name__ == "__main__":
    unittest.main()
