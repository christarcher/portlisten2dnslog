// Package protocol implements the compact, versioned DNS payload shared by
// the Go listener and the Python collector.
package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const (
	Version      byte = 2
	AuthTagSize       = 16
	MaxLabelSize      = 56
	MaxQNameSize      = 253
)

var base32NoPadding = base32.StdEncoding.WithPadding(base32.NoPadding)

type Event struct {
	ID         string
	SourceIP   netip.Addr
	SourcePort uint16
	TargetIP   netip.Addr
	TargetPort uint16
	Time       time.Time
}

// NewUUID returns a lowercase RFC 4122 version 4 UUID without adding a
// third-party dependency.
func NewUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		raw[0:4],
		raw[4:6],
		raw[6:8],
		raw[8:10],
		raw[10:16],
	), nil
}

// Encode serializes an event, appends a truncated HMAC-SHA256 authentication
// tag, applies the configured repeating-key XOR, and returns an unpadded
// lowercase Base32 value that is safe in DNS labels.
func Encode(event Event, xorKey, authKey []byte) (string, error) {
	if len(xorKey) == 0 {
		return "", errors.New("XOR key must not be empty")
	}
	if len(authKey) == 0 {
		return "", errors.New("authentication key must not be empty")
	}
	if !event.SourceIP.IsValid() || !event.TargetIP.IsValid() {
		return "", errors.New("source and target IP must be valid")
	}

	raw := make([]byte, 0, 47+AuthTagSize)
	raw = append(raw, Version)
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(event.Time.Unix()))
	raw = append(raw, timestamp[:]...)

	var err error
	raw, err = appendEndpoint(raw, event.SourceIP, event.SourcePort)
	if err != nil {
		return "", fmt.Errorf("source endpoint: %w", err)
	}
	raw, err = appendEndpoint(raw, event.TargetIP, event.TargetPort)
	if err != nil {
		return "", fmt.Errorf("target endpoint: %w", err)
	}

	mac := hmac.New(sha256.New, authKey)
	_, _ = mac.Write(raw)
	raw = append(raw, mac.Sum(nil)[:AuthTagSize]...)

	for i := range raw {
		raw[i] ^= xorKey[i%len(xorKey)]
	}
	return strings.ToLower(base32NoPadding.EncodeToString(raw)), nil
}

func appendEndpoint(dst []byte, ip netip.Addr, port uint16) ([]byte, error) {
	ip = ip.Unmap()
	if ip.Is4() {
		dst = append(dst, 4)
		value := ip.As4()
		dst = append(dst, value[:]...)
	} else if ip.Is6() {
		dst = append(dst, 6)
		value := ip.As16()
		dst = append(dst, value[:]...)
	} else {
		return nil, errors.New("unsupported IP family")
	}
	var encodedPort [2]byte
	binary.BigEndian.PutUint16(encodedPort[:], port)
	return append(dst, encodedPort[:]...), nil
}

// BuildQName creates:
// UUID.payload-label[.payload-label...].marker.base-domain
func BuildQName(id, payload, marker, baseDomain string) (string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	payload = strings.ToLower(strings.TrimSpace(payload))
	marker = normalizeDomain(marker)
	baseDomain = normalizeDomain(baseDomain)

	if !isUUID(id) {
		return "", errors.New("invalid UUID")
	}
	if payload == "" {
		return "", errors.New("payload must not be empty")
	}
	if !validBase32(payload) {
		return "", errors.New("payload is not DNS-safe Base32")
	}
	if err := ValidateNamespace(marker, baseDomain); err != nil {
		return "", err
	}

	labels := []string{id}
	for len(payload) > 0 {
		size := MaxLabelSize
		if len(payload) < size {
			size = len(payload)
		}
		labels = append(labels, payload[:size])
		payload = payload[size:]
	}
	labels = append(labels, marker)
	labels = append(labels, strings.Split(baseDomain, ".")...)
	qname := strings.Join(labels, ".")
	if len(qname) > MaxQNameSize {
		return "", fmt.Errorf("DNS name is %d bytes, maximum is %d", len(qname), MaxQNameSize)
	}
	return qname, nil
}

// ValidateNamespace validates the fixed marker and DNSLog base domain before
// the listener starts accepting connections.
func ValidateNamespace(marker, baseDomain string) error {
	marker = normalizeDomain(marker)
	baseDomain = normalizeDomain(baseDomain)
	if err := validateDomain(marker); err != nil {
		return fmt.Errorf("marker: %w", err)
	}
	if strings.Contains(marker, ".") {
		return errors.New("marker must be one DNS label")
	}
	if err := validateDomain(baseDomain); err != nil {
		return fmt.Errorf("base domain: %w", err)
	}
	return nil
}

func normalizeDomain(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "."))
}

func validateDomain(value string) error {
	if value == "" {
		return errors.New("must not be empty")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 {
			return errors.New("contains an empty or oversized label")
		}
		for i, char := range label {
			valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-'
			if !valid || (char == '-' && (i == 0 || i == len(label)-1)) {
				return errors.New("contains an invalid DNS label")
			}
		}
	}
	return nil
}

func validBase32(value string) bool {
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= '2' && char <= '7') {
			return false
		}
	}
	return true
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range value {
		switch i {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
				return false
			}
		}
	}
	return true
}
