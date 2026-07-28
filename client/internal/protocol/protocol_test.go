package protocol

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

var testAuthKey = []byte("authentication-test-key-32-bytes!")

func TestNewUUID(t *testing.T) {
	id, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if !isUUID(id) {
		t.Fatalf("NewUUID() returned an invalid value: %q", id)
	}
	if id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("NewUUID() did not return an RFC 4122 v4 UUID: %q", id)
	}
}

func TestEncodeIPv4(t *testing.T) {
	event := Event{
		SourceIP:   netip.MustParseAddr("192.0.2.10"),
		SourcePort: 54321,
		TargetIP:   netip.MustParseAddr("10.0.0.8"),
		TargetPort: 445,
		Time:       time.Unix(1_722_470_400, 0),
	}
	got, err := Encode(event, []byte("test-key"), testAuthKey)
	if err != nil {
		t.Fatal(err)
	}
	const want = "ozsxg5bnbxh2s5dbwn2c6ynrjbyg643uevvnrnyr4fcozfasp35tbhtssi5hsli"
	if got != want {
		t.Fatalf("Encode() = %q, want %q", got, want)
	}
}

func TestBuildQNameSplitsIPv6Payload(t *testing.T) {
	event := Event{
		SourceIP:   netip.MustParseAddr("2001:db8::10"),
		SourcePort: 65000,
		TargetIP:   netip.MustParseAddr("2001:db8::20"),
		TargetPort: 8080,
		Time:       time.Unix(1_722_470_400, 0),
	}
	payload, err := Encode(event, []byte("test-key"), testAuthKey)
	if err != nil {
		t.Fatal(err)
	}
	qname, err := BuildQName(
		"37604be5-aa6f-439b-8c09-cd06efb5b23b",
		payload,
		"listener-req",
		"example.dnslog.test.",
	)
	if err != nil {
		t.Fatal(err)
	}
	labels := strings.Split(qname, ".")
	if len(labels) != 7 {
		t.Fatalf("got %d labels (%q), expected UUID + 2 payload + marker + domain", len(labels), qname)
	}
	if len(labels[1]) != MaxLabelSize || len(labels[2]) > MaxLabelSize {
		t.Fatalf("payload was not split at %d bytes: %q", MaxLabelSize, qname)
	}
}

func TestBuildQNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		payload string
		marker  string
		domain  string
	}{
		{"bad UUID", "not-a-uuid", "abcd2", "listener-req", "example.test"},
		{"bad payload", "37604be5-aa6f-439b-8c09-cd06efb5b23b", "abc_2", "listener-req", "example.test"},
		{"bad marker", "37604be5-aa6f-439b-8c09-cd06efb5b23b", "abcd2", "listener.req", "example.test"},
		{"bad domain", "37604be5-aa6f-439b-8c09-cd06efb5b23b", "abcd2", "listener-req", "-example.test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildQName(test.id, test.payload, test.marker, test.domain); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
