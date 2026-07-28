from __future__ import annotations

import unittest

from portlistener2dns_server.protocol import (
    Event,
    ProtocolError,
    build_qname,
    decode_qname,
    encode_payload,
)


class ProtocolTests(unittest.TestCase):
    def setUp(self) -> None:
        self.key = b"test-key"
        self.auth_key = b"authentication-test-key-32-bytes!"
        self.event = Event(
            event_id="37604be5-aa6f-439b-8c09-cd06efb5b23b",
            source_ip="192.0.2.10",
            source_port=54321,
            target_ip="10.0.0.8",
            target_port=445,
            timestamp=1_722_470_400,
        )

    def test_go_compatible_vector(self) -> None:
        self.assertEqual(
            encode_payload(self.event, self.key, self.auth_key),
            "ozsxg5bnbxh2s5dbwn2c6ynrjbyg643uevvnrnyr4fcozfasp35tbhtssi5hsli",
        )

    def test_round_trip_ipv4(self) -> None:
        qname = build_qname(
            self.event,
            base_domain="abc.dnslog.test",
            marker="listener-req",
            xor_key=self.key,
            auth_key=self.auth_key,
        )
        decoded = decode_qname(
            qname + ".",
            base_domain="abc.dnslog.test.",
            marker="listener-req",
            xor_key=self.key,
            auth_key=self.auth_key,
        )
        self.assertEqual(decoded, self.event)

    def test_round_trip_ipv6_uses_two_payload_labels(self) -> None:
        event = Event(
            event_id=self.event.event_id,
            source_ip="2001:db8::10",
            source_port=65000,
            target_ip="2001:db8::20",
            target_port=8080,
            timestamp=self.event.timestamp,
        )
        qname = build_qname(
            event,
            base_domain="abc.dnslog.test",
            marker="listener-req",
            xor_key=self.key,
            auth_key=self.auth_key,
        )
        labels = qname.split(".")
        self.assertEqual(len(labels[1]), 56)
        self.assertEqual(
            decode_qname(
                qname,
                base_domain="abc.dnslog.test",
                marker="listener-req",
                xor_key=self.key,
                auth_key=self.auth_key,
            ),
            event,
        )

    def test_rejects_other_domain_and_malformed_payload(self) -> None:
        qname = build_qname(
            self.event,
            base_domain="abc.dnslog.test",
            marker="listener-req",
            xor_key=self.key,
            auth_key=self.auth_key,
        )
        with self.assertRaises(ProtocolError):
            decode_qname(
                qname,
                base_domain="other.dnslog.test",
                marker="listener-req",
                xor_key=self.key,
                auth_key=self.auth_key,
            )
        labels = qname.split(".")
        labels[1] = "bad_payload"
        with self.assertRaises(ProtocolError):
            decode_qname(
                ".".join(labels),
                base_domain="abc.dnslog.test",
                marker="listener-req",
                xor_key=self.key,
                auth_key=self.auth_key,
            )

    def test_rejects_timestamp_outside_sqlite_and_datetime_range(self) -> None:
        event = Event(
            event_id=self.event.event_id,
            source_ip=self.event.source_ip,
            source_port=self.event.source_port,
            target_ip=self.event.target_ip,
            target_port=self.event.target_port,
            timestamp=0xFFFFFFFFFFFFFFFF,
        )
        qname = build_qname(
            event,
            base_domain="abc.dnslog.test",
            marker="listener-req",
            xor_key=self.key,
            auth_key=self.auth_key,
        )
        with self.assertRaises(ProtocolError):
            decode_qname(
                qname,
                base_domain="abc.dnslog.test",
                marker="listener-req",
                xor_key=self.key,
                auth_key=self.auth_key,
            )

    def test_rejects_tampered_authenticated_payload(self) -> None:
        qname = build_qname(
            self.event,
            base_domain="abc.dnslog.test",
            marker="listener-req",
            xor_key=self.key,
            auth_key=self.auth_key,
        )
        labels = qname.split(".")
        labels[1] = ("a" if labels[1][0] != "a" else "b") + labels[1][1:]
        with self.assertRaisesRegex(ProtocolError, "authentication failed"):
            decode_qname(
                ".".join(labels),
                base_domain="abc.dnslog.test",
                marker="listener-req",
                xor_key=self.key,
                auth_key=self.auth_key,
            )


if __name__ == "__main__":
    unittest.main()
