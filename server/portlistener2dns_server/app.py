"""DNSLog polling, durable deduplication, and Telegram notification."""

from __future__ import annotations

import json
import logging
import os
import signal
import sqlite3
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Iterable

from .protocol import Event, ProtocolError, decode_qname, normalize_dns_name

LOGGER = logging.getLogger("portlistener2dns-server")
MAX_HTTP_RESPONSE = 8 * 1024 * 1024


@dataclass(frozen=True, slots=True)
class DNSLogRecord:
    subdomain: str
    observed_at: str
    resolver_address: str


@dataclass(frozen=True, slots=True)
class Settings:
    dnslog_token: str
    base_domain: str
    xor_key: bytes
    auth_key: bytes
    database_path: Path
    telegram_token: str
    telegram_chat_id: str
    marker: str = "listener-req"
    poll_interval: float = 10.0
    request_timeout: float = 10.0
    pending_batch: int = 100
    dnslog_url: str = "https://dnslog.org/{token}"
    telegram_api_base: str = "https://api.telegram.org"
    disable_notification: bool = False


@dataclass(frozen=True, slots=True)
class PendingEvent:
    event: Event
    observed_at: str
    resolver_address: str
    attempts: int


class EventStore:
    def __init__(self, path: Path) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        self._connection = sqlite3.connect(path, timeout=30)
        self._connection.row_factory = sqlite3.Row
        self._connection.execute("PRAGMA journal_mode = WAL")
        self._connection.execute("PRAGMA synchronous = NORMAL")
        self._connection.execute("PRAGMA busy_timeout = 30000")
        self._connection.execute(
            """
            CREATE TABLE IF NOT EXISTS events (
                event_id TEXT PRIMARY KEY,
                source_ip TEXT NOT NULL,
                source_port INTEGER NOT NULL,
                target_ip TEXT NOT NULL,
                target_port INTEGER NOT NULL,
                event_timestamp INTEGER NOT NULL,
                observed_at TEXT NOT NULL,
                resolver_address TEXT NOT NULL,
                status TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sent')),
                attempts INTEGER NOT NULL DEFAULT 0,
                last_error TEXT,
                created_at TEXT NOT NULL,
                sent_at TEXT
            )
            """
        )
        self._connection.execute(
            "CREATE INDEX IF NOT EXISTS events_pending_idx "
            "ON events(status, created_at)"
        )
        self._connection.commit()

    def close(self) -> None:
        self._connection.close()

    def add(
        self,
        event: Event,
        *,
        observed_at: str,
        resolver_address: str,
    ) -> bool:
        created_at = datetime.now(UTC).isoformat()
        with self._connection:
            cursor = self._connection.execute(
                """
                INSERT OR IGNORE INTO events (
                    event_id, source_ip, source_port, target_ip, target_port,
                    event_timestamp, observed_at, resolver_address, created_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    event.event_id,
                    event.source_ip,
                    event.source_port,
                    event.target_ip,
                    event.target_port,
                    event.timestamp,
                    observed_at,
                    resolver_address,
                    created_at,
                ),
            )
        return cursor.rowcount == 1

    def pending(self, limit: int) -> list[PendingEvent]:
        rows = self._connection.execute(
            """
            SELECT event_id, source_ip, source_port, target_ip, target_port,
                   event_timestamp, observed_at, resolver_address, attempts
              FROM events
             WHERE status = 'pending'
             ORDER BY created_at, event_id
             LIMIT ?
            """,
            (limit,),
        ).fetchall()
        return [
            PendingEvent(
                event=Event(
                    event_id=row["event_id"],
                    source_ip=row["source_ip"],
                    source_port=row["source_port"],
                    target_ip=row["target_ip"],
                    target_port=row["target_port"],
                    timestamp=row["event_timestamp"],
                ),
                observed_at=row["observed_at"],
                resolver_address=row["resolver_address"],
                attempts=row["attempts"],
            )
            for row in rows
        ]

    def mark_sent(self, event_id: str) -> None:
        with self._connection:
            self._connection.execute(
                """
                UPDATE events
                   SET status = 'sent', attempts = attempts + 1,
                       last_error = NULL, sent_at = ?
                 WHERE event_id = ? AND status = 'pending'
                """,
                (datetime.now(UTC).isoformat(), event_id),
            )

    def mark_failed(self, event_id: str, error: str) -> None:
        with self._connection:
            self._connection.execute(
                """
                UPDATE events
                   SET attempts = attempts + 1, last_error = ?
                 WHERE event_id = ? AND status = 'pending'
                """,
                (error[:1000], event_id),
            )

    def status(self, event_id: str) -> str | None:
        row = self._connection.execute(
            "SELECT status FROM events WHERE event_id = ?",
            (event_id,),
        ).fetchone()
        return None if row is None else str(row["status"])


class DNSLogClient:
    def __init__(self, settings: Settings) -> None:
        self._timeout = settings.request_timeout
        quoted_token = urllib.parse.quote(settings.dnslog_token, safe="")
        self._url = settings.dnslog_url.replace("{token}", quoted_token)

    def fetch(self) -> list[DNSLogRecord]:
        request = urllib.request.Request(
            self._url,
            headers={
                "Accept": "application/json",
                "User-Agent": "portlistener2dns-server/1.0",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=self._timeout) as response:
                content = response.read(MAX_HTTP_RESPONSE + 1)
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise RuntimeError(f"DNSLog 请求失败: {_network_error(exc)}") from exc
        if len(content) > MAX_HTTP_RESPONSE:
            raise RuntimeError("DNSLog 响应超过 8 MiB 限制")
        try:
            value = json.loads(content)
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            raise RuntimeError("DNSLog 返回了无效 JSON") from exc
        return list(_parse_dnslog_response(value))


class TelegramClient:
    def __init__(self, settings: Settings) -> None:
        quoted_token = urllib.parse.quote(settings.telegram_token, safe="")
        self._url = (
            f"{settings.telegram_api_base.rstrip('/')}/bot{quoted_token}/sendMessage"
        )
        self._chat_id = settings.telegram_chat_id
        self._disable_notification = settings.disable_notification
        self._timeout = settings.request_timeout

    def send(self, pending: PendingEvent) -> None:
        body = urllib.parse.urlencode(
            {
                "chat_id": self._chat_id,
                "text": format_message(pending),
                "disable_notification": str(self._disable_notification).lower(),
            }
        ).encode("utf-8")
        request = urllib.request.Request(
            self._url,
            data=body,
            headers={
                "Content-Type": "application/x-www-form-urlencoded",
                "User-Agent": "portlistener2dns-server/1.0",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self._timeout) as response:
                content = response.read(1024 * 1024)
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise RuntimeError(f"Telegram 请求失败: {_network_error(exc)}") from exc
        try:
            result = json.loads(content)
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            raise RuntimeError("Telegram 返回了无效 JSON") from exc
        if not isinstance(result, dict) or result.get("ok") is not True:
            description = result.get("description", "未知错误") if isinstance(result, dict) else "未知错误"
            raise RuntimeError(f"Telegram 拒绝了消息: {description}")


class Collector:
    def __init__(
        self,
        settings: Settings,
        store: EventStore,
        dnslog: DNSLogClient,
        telegram: TelegramClient,
    ) -> None:
        self._settings = settings
        self._store = store
        self._dnslog = dnslog
        self._telegram = telegram

    def cycle(self) -> bool:
        success = True
        added = 0
        try:
            records = self._dnslog.fetch()
        except RuntimeError as exc:
            LOGGER.error("%s", exc)
            records = []
            success = False

        for record in records:
            try:
                event = decode_qname(
                    record.subdomain,
                    base_domain=self._settings.base_domain,
                    marker=self._settings.marker,
                    xor_key=self._settings.xor_key,
                    auth_key=self._settings.auth_key,
                )
            except ProtocolError as exc:
                LOGGER.debug("忽略 DNSLog 记录 %r: %s", record.subdomain, exc)
                continue
            if self._store.add(
                event,
                observed_at=record.observed_at,
                resolver_address=record.resolver_address,
            ):
                added += 1

        if added:
            LOGGER.info("记录了 %d 个新事件", added)

        for pending in self._store.pending(self._settings.pending_batch):
            try:
                self._telegram.send(pending)
            except RuntimeError as exc:
                self._store.mark_failed(pending.event.event_id, str(exc))
                LOGGER.error("发送事件 %s 失败: %s", pending.event.event_id, exc)
                success = False
                # A bot/network failure is normally shared by all pending events.
                # Stop this batch and retry it during the next poll interval.
                break
            self._store.mark_sent(pending.event.event_id)
            LOGGER.info("已发送事件 %s", pending.event.event_id)
        return success


def format_message(pending: PendingEvent) -> str:
    event = pending.event
    try:
        event_time = datetime.fromtimestamp(event.timestamp, UTC).isoformat()
    except (OverflowError, OSError, ValueError):
        event_time = f"Unix {event.timestamp}"
    return "\n".join(
        [
            "🚨 内网端口扫描告警",
            f"源：{_format_endpoint(event.source_ip, event.source_port)}",
            f"目标：{_format_endpoint(event.target_ip, event.target_port)}",
            f"连接时间：{event_time}",
            f"事件 UUID：{event.event_id}",
            f"DNSLog 时间：{pending.observed_at or '未知'}",
        ]
    )


def load_settings_from_env() -> tuple[Settings, bool, bool, bool]:
    names = (
        "P2D_DNSLOG_TOKEN",
        "P2D_DOMAIN",
        "P2D_XOR_KEY",
        "P2D_AUTH_KEY",
        "P2D_DB_PATH",
        "P2D_MARKER",
        "P2D_POLL_INTERVAL",
        "P2D_REQUEST_TIMEOUT",
        "P2D_PENDING_BATCH",
        "P2D_DNSLOG_URL",
        "P2D_TELEGRAM_BOT_TOKEN",
        "P2D_TELEGRAM_CHAT_ID",
        "P2D_TELEGRAM_API_BASE",
        "P2D_TELEGRAM_DISABLE_NOTIFICATION",
        "P2D_RUN_ONCE",
        "P2D_CHECK_CONFIG",
        "P2D_VERBOSE",
    )
    values = {name: os.environ.pop(name, "") for name in names}

    token = _required_env(values, "P2D_DNSLOG_TOKEN")
    base_domain = normalize_dns_name(_required_env(values, "P2D_DOMAIN"))
    marker = normalize_dns_name(
        _env_or_default(values, "P2D_MARKER", "listener-req"),
        single_label=True,
    )
    xor_key_text = _required_env(values, "P2D_XOR_KEY")
    if len(xor_key_text.encode("utf-8")) < 8:
        raise ValueError("P2D_XOR_KEY 至少需要 8 个 UTF-8 字节")
    auth_key_text = _required_env(values, "P2D_AUTH_KEY")
    if len(auth_key_text.encode("utf-8")) < 32:
        raise ValueError("P2D_AUTH_KEY 至少需要 32 个 UTF-8 字节")

    database_value = _env_or_default(values, "P2D_DB_PATH", "events.db")
    database_path = Path(database_value).expanduser()
    if not database_path.is_absolute():
        database_path = Path.cwd() / database_path

    poll_interval = _positive_float_env(values, "P2D_POLL_INTERVAL", 10.0)
    request_timeout = _positive_float_env(values, "P2D_REQUEST_TIMEOUT", 10.0)
    pending_batch = _positive_int_env(values, "P2D_PENDING_BATCH", 100)
    dnslog_url = _env_or_default(
        values,
        "P2D_DNSLOG_URL",
        "https://dnslog.org/{token}",
    )
    if "{token}" not in dnslog_url:
        raise ValueError("P2D_DNSLOG_URL 必须包含 {token}")

    telegram_token = _required_env(values, "P2D_TELEGRAM_BOT_TOKEN")
    telegram_chat_id = _required_env(values, "P2D_TELEGRAM_CHAT_ID")
    telegram_api_base = _env_or_default(
        values,
        "P2D_TELEGRAM_API_BASE",
        "https://api.telegram.org",
    )
    disable_notification = _bool_env(
        values,
        "P2D_TELEGRAM_DISABLE_NOTIFICATION",
        False,
    )

    settings = Settings(
        dnslog_token=token,
        base_domain=base_domain,
        marker=marker,
        xor_key=xor_key_text.encode("utf-8"),
        auth_key=auth_key_text.encode("utf-8"),
        database_path=database_path,
        poll_interval=poll_interval,
        request_timeout=request_timeout,
        pending_batch=pending_batch,
        dnslog_url=dnslog_url,
        telegram_token=telegram_token,
        telegram_chat_id=telegram_chat_id,
        telegram_api_base=telegram_api_base,
        disable_notification=disable_notification,
    )
    return (
        settings,
        _bool_env(values, "P2D_RUN_ONCE", False),
        _bool_env(values, "P2D_CHECK_CONFIG", False),
        _bool_env(values, "P2D_VERBOSE", False),
    )


def run(settings: Settings, *, once: bool = False) -> int:
    store = EventStore(settings.database_path)
    collector = Collector(
        settings,
        store,
        DNSLogClient(settings),
        TelegramClient(settings),
    )
    stop = threading.Event()

    def request_stop(_signum: int, _frame: Any) -> None:
        stop.set()

    previous_handlers: dict[signal.Signals, Any] = {}
    for signal_name in (signal.SIGINT, signal.SIGTERM):
        previous_handlers[signal_name] = signal.signal(signal_name, request_stop)

    try:
        if once:
            return 0 if collector.cycle() else 1
        LOGGER.info("服务已启动；每 %.1f 秒轮询一次", settings.poll_interval)
        while not stop.is_set():
            collector.cycle()
            stop.wait(settings.poll_interval)
        LOGGER.info("服务已停止")
        return 0
    finally:
        store.close()
        for signal_name, handler in previous_handlers.items():
            signal.signal(signal_name, handler)


def main() -> int:
    try:
        settings, run_once, check_config, verbose = load_settings_from_env()
    except (ValueError, ProtocolError) as exc:
        logging.basicConfig(
            level=logging.INFO,
            format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        )
        LOGGER.error("配置错误: %s", exc)
        return 2
    logging.basicConfig(
        level=logging.DEBUG if verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    if check_config:
        LOGGER.info("配置有效")
        return 0
    try:
        return run(settings, once=run_once)
    except (OSError, sqlite3.Error) as exc:
        LOGGER.error("运行失败: %s", exc)
        return 1


def _parse_dnslog_response(value: Any) -> Iterable[DNSLogRecord]:
    if isinstance(value, dict):
        records = value.values()
    elif isinstance(value, list):
        records = value
    else:
        raise RuntimeError("DNSLog JSON 顶层必须是对象或数组")
    for record in records:
        if not isinstance(record, dict):
            continue
        subdomain = record.get("subdomain")
        if not isinstance(subdomain, str) or not subdomain.strip():
            continue
        observed_at = record.get("time", "")
        resolver_address = record.get("ip", "")
        yield DNSLogRecord(
            subdomain=subdomain,
            observed_at=observed_at[:128] if isinstance(observed_at, str) else "",
            resolver_address=(
                resolver_address[:128] if isinstance(resolver_address, str) else ""
            ),
        )


def _format_endpoint(address: str, port: int) -> str:
    if ":" in address:
        return f"[{address}]:{port}"
    return f"{address}:{port}"


def _network_error(exc: BaseException) -> str:
    if isinstance(exc, urllib.error.HTTPError):
        return f"HTTP {exc.code}"
    if isinstance(exc, urllib.error.URLError):
        return str(exc.reason)
    return str(exc)


def _required_env(values: dict[str, str], name: str) -> str:
    value = values.get(name, "").strip()
    if not value:
        raise ValueError(f"{name} 不能为空")
    return value


def _env_or_default(values: dict[str, str], name: str, default: str) -> str:
    value = values.get(name, "").strip()
    return value or default


def _positive_float_env(
    values: dict[str, str],
    name: str,
    default: float,
) -> float:
    value = values.get(name, "").strip()
    if not value:
        return default
    try:
        parsed = float(value)
    except ValueError as exc:
        raise ValueError(f"{name} 必须是正数") from exc
    if parsed <= 0:
        raise ValueError(f"{name} 必须是正数")
    return parsed


def _positive_int_env(values: dict[str, str], name: str, default: int) -> int:
    value = values.get(name, "").strip()
    if not value:
        return default
    try:
        parsed = int(value)
    except ValueError as exc:
        raise ValueError(f"{name} 必须是正整数") from exc
    if parsed <= 0:
        raise ValueError(f"{name} 必须是正整数")
    return parsed


def _bool_env(values: dict[str, str], name: str, default: bool) -> bool:
    value = values.get(name, "").strip().lower()
    if not value:
        return default
    if value in {"1", "true", "yes", "on"}:
        return True
    if value in {"0", "false", "no", "off"}:
        return False
    raise ValueError(f"{name} 必须是 true 或 false")


if __name__ == "__main__":
    sys.exit(main())
