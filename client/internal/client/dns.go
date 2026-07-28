package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// sendDNSQuery sends one minimal recursive A query and accepts every valid DNS
// response, including NXDOMAIN. An NXDOMAIN still proves that the query reached
// the configured resolver and, therefore, the DNSLog authoritative server.
func sendDNSQuery(ctx context.Context, server, qname string) error {
	packet, transactionID, err := buildDNSQuery(qname)
	if err != nil {
		return err
	}

	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "udp", server)
	if err != nil {
		return fmt.Errorf("connect to DNS server: %w", err)
	}
	defer connection.Close()

	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set DNS deadline: %w", err)
		}
	}
	if _, err := connection.Write(packet); err != nil {
		return fmt.Errorf("send DNS query: %w", err)
	}

	response := make([]byte, 4096)
	size, err := connection.Read(response)
	if err != nil {
		return fmt.Errorf("read DNS response: %w", err)
	}
	if size < 12 {
		return errors.New("DNS response is shorter than its header")
	}
	if binary.BigEndian.Uint16(response[:2]) != transactionID {
		return errors.New("DNS response transaction ID mismatch")
	}
	flags := binary.BigEndian.Uint16(response[2:4])
	if flags&0x8000 == 0 {
		return errors.New("received a DNS packet that is not a response")
	}
	return nil
}

func buildDNSQuery(qname string) ([]byte, uint16, error) {
	qname = strings.Trim(strings.TrimSpace(qname), ".")
	if qname == "" || len(qname) > 253 {
		return nil, 0, errors.New("invalid DNS query name length")
	}

	var random [2]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, 0, fmt.Errorf("generate DNS transaction ID: %w", err)
	}
	transactionID := binary.BigEndian.Uint16(random[:])

	packet := make([]byte, 12, 12+len(qname)+6)
	binary.BigEndian.PutUint16(packet[0:2], transactionID)
	binary.BigEndian.PutUint16(packet[2:4], 0x0100) // recursion desired
	binary.BigEndian.PutUint16(packet[4:6], 1)      // one question

	for _, label := range strings.Split(qname, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, 0, errors.New("DNS name contains an empty or oversized label")
		}
		packet = append(packet, byte(len(label)))
		packet = append(packet, label...)
	}
	packet = append(packet, 0)
	packet = append(packet, 0, 1) // QTYPE A
	packet = append(packet, 0, 1) // QCLASS IN
	return packet, transactionID, nil
}

func sendWithRetry(
	ctx context.Context,
	server string,
	qname string,
	retries int,
	timeout time.Duration,
) error {
	var lastError error
	for attempt := 1; attempt <= retries; attempt++ {
		queryContext, cancel := context.WithTimeout(ctx, timeout)
		lastError = sendDNSQuery(queryContext, server, qname)
		cancel()
		if lastError == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < retries {
			delay := time.Duration(attempt) * 250 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastError
}
