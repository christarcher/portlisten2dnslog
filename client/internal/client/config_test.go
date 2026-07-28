package client

import (
	"os"
	"testing"
)

func TestConfigFromEnvAndUnset(t *testing.T) {
	t.Setenv("P2D_DOMAIN", "abc.dnslog.test.")
	t.Setenv("P2D_DNS_SERVER", "127.0.0.1:5353")
	t.Setenv("P2D_XOR_KEY", "test-key")
	t.Setenv("P2D_AUTH_KEY", "authentication-test-key-32-bytes!")
	t.Setenv("P2D_BIND_ADDRESS", "127.0.0.1")
	t.Setenv("P2D_PORTS", "139,445,1080,1099")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Domain != "abc.dnslog.test" || cfg.DNSServer != "127.0.0.1:5353" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	wantListen := []string{"127.0.0.1:139", "127.0.0.1:445", "127.0.0.1:1080", "127.0.0.1:1099"}
	if len(cfg.Listen) != len(wantListen) {
		t.Fatalf("got listen addresses %v, want %v", cfg.Listen, wantListen)
	}
	for index := range wantListen {
		if cfg.Listen[index] != wantListen[index] {
			t.Fatalf("got listen addresses %v, want %v", cfg.Listen, wantListen)
		}
	}
	for _, name := range []string{
		"P2D_DOMAIN",
		"P2D_DNS_SERVER",
		"P2D_XOR_KEY",
		"P2D_AUTH_KEY",
		"P2D_BIND_ADDRESS",
		"P2D_PORTS",
	} {
		if _, exists := os.LookupEnv(name); exists {
			t.Fatalf("%s was not unset", name)
		}
	}
}

func TestConfigRejectsHostnameDNSServer(t *testing.T) {
	t.Setenv("P2D_DOMAIN", "abc.dnslog.test")
	t.Setenv("P2D_DNS_SERVER", "dns.example.test")
	t.Setenv("P2D_XOR_KEY", "test-key")
	t.Setenv("P2D_AUTH_KEY", "authentication-test-key-32-bytes!")

	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected invalid DNS server error")
	}
}

func TestConfigRejectsDuplicatePort(t *testing.T) {
	t.Setenv("P2D_DOMAIN", "abc.dnslog.test")
	t.Setenv("P2D_DNS_SERVER", "127.0.0.1")
	t.Setenv("P2D_XOR_KEY", "test-key")
	t.Setenv("P2D_AUTH_KEY", "authentication-test-key-32-bytes!")
	t.Setenv("P2D_PORTS", "445,445")

	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected duplicate port error")
	}
}
