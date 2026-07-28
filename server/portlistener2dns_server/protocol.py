"""Versioned DNS payload codec shared with the Go listener."""

from __future__ import annotations

import base64
import binascii
import hashlib
import hmac
import ipaddress
import re
import struct
import uuid
from dataclasses import dataclass

VERSION = 2
AUTH_TAG_SIZE = 16
MAX_PAYLOAD_LABEL = 56
MAX_QNAME = 253
_BASE32_RE = re.compile(r"^[a-z2-7]+$")
_DNS_LABEL_RE = re.compile(r"^(?!-)[a-z0-9-]{1,63}(?<!-)$")


class ProtocolError(ValueError):
    """Raised when an untrusted DNS name or payload is invalid."""


@dataclass(frozen=True, slots=True)
class Event:
    event_id: str
    source_ip: str
    source_port: int
    target_ip: str
    target_port: int
    timestamp: int


def normalize_dns_name(value: str, *, single_label: bool = False) -> str:
    normalized = value.strip().strip(".").lower()
    if not normalized:
        raise ProtocolError("DNS name must not be empty")
    labels = normalized.split(".")
    if single_label and len(labels) != 1:
        raise ProtocolError("value must be one DNS label")
    if any(_DNS_LABEL_RE.fullmatch(label) is None for label in labels):
        raise ProtocolError("DNS name contains an invalid label")
    return normalized


def decode_qname(
    qname: str,
    *,
    base_domain: str,
    marker: str,
    xor_key: bytes,
    auth_key: bytes,
) -> Event:
    """Validate a DNSLog qname and decode one event."""
    if not xor_key:
        raise ProtocolError("XOR key must not be empty")
    if not auth_key:
        raise ProtocolError("authentication key must not be empty")
    normalized = qname.strip().strip(".").lower()
    if not normalized or len(normalized) > MAX_QNAME:
        raise ProtocolError("invalid DNS name length")

    labels = normalized.split(".")
    domain_labels = normalize_dns_name(base_domain).split(".")
    marker_label = normalize_dns_name(marker, single_label=True)
    suffix = [marker_label, *domain_labels]
    if len(labels) < len(suffix) + 2 or labels[-len(suffix) :] != suffix:
        raise ProtocolError("DNS name does not belong to this collector")

    event_id = _canonical_uuid(labels[0])
    payload_labels = labels[1 : -len(suffix)]
    if any(
        len(label) > MAX_PAYLOAD_LABEL or _BASE32_RE.fullmatch(label) is None
        for label in payload_labels
    ):
        raise ProtocolError("invalid payload label")

    encoded = "".join(payload_labels)
    padding = "=" * ((8 - len(encoded) % 8) % 8)
    try:
        encrypted = base64.b32decode((encoded + padding).upper(), casefold=False)
    except (binascii.Error, ValueError) as exc:
        raise ProtocolError("invalid Base32 payload") from exc
    raw = _xor(encrypted, xor_key)
    return _decode_payload(event_id, raw, auth_key)


def encode_payload(event: Event, xor_key: bytes, auth_key: bytes) -> str:
    """Encode an event; used by tests and protocol interoperability tools."""
    if not xor_key:
        raise ProtocolError("XOR key must not be empty")
    if not auth_key:
        raise ProtocolError("authentication key must not be empty")
    source = ipaddress.ip_address(event.source_ip)
    target = ipaddress.ip_address(event.target_ip)
    if not 0 <= event.timestamp <= 0xFFFFFFFFFFFFFFFF:
        raise ProtocolError("timestamp is outside uint64")

    raw = bytearray([VERSION])
    raw.extend(struct.pack(">Q", event.timestamp))
    raw.extend(_encode_endpoint(source, event.source_port))
    raw.extend(_encode_endpoint(target, event.target_port))
    raw.extend(hmac.digest(auth_key, bytes(raw), hashlib.sha256)[:AUTH_TAG_SIZE])
    return base64.b32encode(_xor(bytes(raw), xor_key)).decode("ascii").rstrip("=").lower()


def build_qname(
    event: Event,
    *,
    base_domain: str,
    marker: str,
    xor_key: bytes,
    auth_key: bytes,
) -> str:
    event_id = _canonical_uuid(event.event_id)
    payload = encode_payload(event, xor_key, auth_key)
    payload_labels = [
        payload[position : position + MAX_PAYLOAD_LABEL]
        for position in range(0, len(payload), MAX_PAYLOAD_LABEL)
    ]
    result = ".".join(
        [
            event_id,
            *payload_labels,
            normalize_dns_name(marker, single_label=True),
            normalize_dns_name(base_domain),
        ]
    )
    if len(result) > MAX_QNAME:
        raise ProtocolError("DNS name is too long")
    return result


def _decode_payload(event_id: str, raw: bytes, auth_key: bytes) -> Event:
    if len(raw) < 1 + 8 + 1 + 4 + 2 + 1 + 4 + 2 + AUTH_TAG_SIZE:
        raise ProtocolError("payload is too short")
    message, received_tag = raw[:-AUTH_TAG_SIZE], raw[-AUTH_TAG_SIZE:]
    expected_tag = hmac.digest(auth_key, message, hashlib.sha256)[:AUTH_TAG_SIZE]
    if not hmac.compare_digest(received_tag, expected_tag):
        raise ProtocolError("payload authentication failed")
    if message[0] != VERSION:
        raise ProtocolError(f"unsupported protocol version: {message[0]}")

    timestamp = struct.unpack_from(">Q", message, 1)[0]
    # Keep the value representable both by SQLite's signed INTEGER and by the
    # Python datetime used in notifications (year 9999 maximum).
    if timestamp > 253_402_300_799:
        raise ProtocolError("event timestamp is outside the supported range")
    position = 9
    source_ip, source_port, position = _decode_endpoint(message, position)
    target_ip, target_port, position = _decode_endpoint(message, position)
    if position != len(message):
        raise ProtocolError("payload has trailing data")
    return Event(
        event_id=event_id,
        source_ip=source_ip,
        source_port=source_port,
        target_ip=target_ip,
        target_port=target_port,
        timestamp=timestamp,
    )


def _decode_endpoint(raw: bytes, position: int) -> tuple[str, int, int]:
    if position >= len(raw):
        raise ProtocolError("payload ends before endpoint family")
    family = raw[position]
    position += 1
    if family == 4:
        size = 4
    elif family == 6:
        size = 16
    else:
        raise ProtocolError(f"unsupported IP family: {family}")
    if position + size + 2 > len(raw):
        raise ProtocolError("payload ends inside endpoint")

    address = str(ipaddress.ip_address(raw[position : position + size]))
    position += size
    port = struct.unpack_from(">H", raw, position)[0]
    position += 2
    if port == 0:
        raise ProtocolError("endpoint port must not be zero")
    return address, port, position


def _encode_endpoint(
    address: ipaddress.IPv4Address | ipaddress.IPv6Address,
    port: int,
) -> bytes:
    if not 1 <= port <= 65535:
        raise ProtocolError("endpoint port must be between 1 and 65535")
    family = 4 if address.version == 4 else 6
    return bytes([family]) + address.packed + struct.pack(">H", port)


def _canonical_uuid(value: str) -> str:
    try:
        parsed = uuid.UUID(value)
    except (ValueError, AttributeError) as exc:
        raise ProtocolError("invalid event UUID") from exc
    canonical = str(parsed)
    if value.lower() != canonical:
        raise ProtocolError("event UUID is not canonical")
    return canonical


def _xor(value: bytes, key: bytes) -> bytes:
    return bytes(byte ^ key[index % len(key)] for index, byte in enumerate(value))
