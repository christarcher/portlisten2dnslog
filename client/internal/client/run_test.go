package client

import (
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
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
