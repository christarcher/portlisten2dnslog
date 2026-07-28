package client

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSendDNSQueryAcceptsNXDOMAIN(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	received := make(chan string, 1)
	go func() {
		buffer := make([]byte, 2048)
		size, address, readErr := server.ReadFrom(buffer)
		if readErr != nil {
			return
		}
		received <- decodeQuestionName(buffer[:size])

		response := append([]byte(nil), buffer[:size]...)
		flags := binary.BigEndian.Uint16(response[2:4])
		binary.BigEndian.PutUint16(response[2:4], flags|0x8000|0x0003)
		_, _ = server.WriteTo(response, address)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	const qname = "id.payload.listener-req.example.test"
	if err := sendDNSQuery(ctx, server.LocalAddr().String(), qname); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != qname {
		t.Fatalf("server received %q, want %q", got, qname)
	}
}

func TestBuildDNSQueryRejectsOversizedLabel(t *testing.T) {
	if _, _, err := buildDNSQuery(strings.Repeat("a", 64) + ".test"); err == nil {
		t.Fatal("expected oversized-label error")
	}
}

func decodeQuestionName(packet []byte) string {
	var labels []string
	for position := 12; position < len(packet) && packet[position] != 0; {
		size := int(packet[position])
		position++
		if position+size > len(packet) {
			return ""
		}
		labels = append(labels, string(packet[position:position+size]))
		position += size
	}
	return strings.Join(labels, ".")
}
