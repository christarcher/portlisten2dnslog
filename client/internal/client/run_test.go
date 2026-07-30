package client

import (
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesConnectionAndSendsDNSQuery(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddress := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	dnsServer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dnsServer.Close()
	receivedName := make(chan string, 1)
	go func() {
		buffer := make([]byte, 2048)
		size, address, readErr := dnsServer.ReadFrom(buffer)
		if readErr != nil {
			return
		}
		receivedName <- decodeQuestionName(buffer[:size])
		response := append([]byte(nil), buffer[:size]...)
		flags := binary.BigEndian.Uint16(response[2:4])
		binary.BigEndian.PutUint16(response[2:4], flags|0x8000|0x0003)
		_, _ = dnsServer.WriteTo(response, address)
	}()

	cfg := Config{
		Domain:      "abc.dnslog.test",
		DNSServer:   dnsServer.LocalAddr().String(),
		XORKey:      []byte("test-key"),
		AuthKey:     []byte("authentication-test-key-32-bytes!"),
		Listen:      []string{listenAddress},
		Marker:      "listener-req",
		QueueSize:   8,
		Workers:     1,
		Retries:     1,
		QueryTimout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- Run(ctx, cfg, log.New(io.Discard, "", 0))
	}()

	deadline := time.Now().Add(2 * time.Second)
	var connection net.Conn
	for time.Now().Before(deadline) {
		connection, err = net.DialTimeout("tcp", listenAddress, 50*time.Millisecond)
		if err == nil {
			break
		}
		select {
		case runErr := <-runResult:
			t.Fatalf("Run exited before accepting a connection: %v", runErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("could not connect to listener: %v", err)
	}
	_ = connection.Close()

	select {
	case qname := <-receivedName:
		if !strings.HasSuffix(qname, ".listener-req.abc.dnslog.test") {
			t.Fatalf("unexpected DNS query name: %q", qname)
		}
		labels := strings.Split(qname, ".")
		if len(labels[0]) != 36 || len(labels[1]) == 0 {
			t.Fatalf("query does not contain a UUID and payload: %q", qname)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for DNS query")
	}

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunIgnoresWhitelistedSourceIP(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddress := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	dnsServer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dnsServer.Close()

	cfg := Config{
		Domain:      "abc.dnslog.test",
		DNSServer:   dnsServer.LocalAddr().String(),
		XORKey:      []byte("test-key"),
		AuthKey:     []byte("authentication-test-key-32-bytes!"),
		Listen:      []string{listenAddress},
		IPWhitelist: map[netip.Addr]struct{}{netip.MustParseAddr("127.0.0.1"): {}},
		Marker:      "listener-req",
		QueueSize:   8,
		Workers:     1,
		Retries:     1,
		QueryTimout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- Run(ctx, cfg, log.New(io.Discard, "", 0))
	}()

	deadline := time.Now().Add(2 * time.Second)
	var connection net.Conn
	for time.Now().Before(deadline) {
		connection, err = net.DialTimeout("tcp", listenAddress, 50*time.Millisecond)
		if err == nil {
			break
		}
		select {
		case runErr := <-runResult:
			t.Fatalf("Run exited before accepting a connection: %v", runErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("could not connect to listener: %v", err)
	}
	_ = connection.Close()

	if err := dnsServer.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		cancel()
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	if _, _, err := dnsServer.ReadFrom(buffer); err == nil {
		cancel()
		t.Fatal("received a DNS query for a whitelisted source IP")
	} else if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
		cancel()
		t.Fatalf("waiting for DNS query: %v", err)
	}

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunReplacesOccupiedAutomaticPort(t *testing.T) {
	primaryReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	primaryAddress := primaryReservation.Addr().String()
	if err := primaryReservation.Close(); err != nil {
		t.Fatal(err)
	}

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	replacementReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	replacementAddress := replacementReservation.Addr().String()
	if err := replacementReservation.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Listen:         []string{primaryAddress, occupied.Addr().String()},
		FallbackListen: []string{replacementAddress},
		IPWhitelist:    map[netip.Addr]struct{}{netip.MustParseAddr("127.0.0.1"): {}},
		QueueSize:      1,
		Workers:        1,
		QueryTimout:    time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- Run(ctx, cfg, log.New(io.Discard, "", 0))
	}()

	primaryConnection := waitForTCPListener(t, primaryAddress, runResult)
	_ = primaryConnection.Close()
	replacementConnection := waitForTCPListener(t, replacementAddress, runResult)
	_ = replacementConnection.Close()
	cancel()

	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunRejectsOccupiedExplicitPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	cfg := Config{Listen: []string{occupied.Addr().String()}}
	err = Run(context.Background(), cfg, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("Run accepted an occupied explicit port")
	}
}

func waitForTCPListener(t *testing.T, address string, runResult <-chan error) net.Conn {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			return connection
		}
		select {
		case runErr := <-runResult:
			t.Fatalf("Run exited before listening on replacement port: %v", runErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("could not connect to replacement listener %s", address)
	return nil
}
