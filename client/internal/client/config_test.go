package client

import (
	"net"
	"net/netip"
	"os"
	"strconv"
	"testing"
)

func TestConfigFromEnvAndUnset(t *testing.T) {
	t.Setenv("P2D_DOMAIN", "abc.dnslog.test.")
	t.Setenv("P2D_DNS_SERVER", "127.0.0.1:5353")
	t.Setenv("P2D_IP_WHITELIST", "192.0.2.10,2001:db8::10")
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
	if len(cfg.FallbackListen) != 0 {
		t.Fatalf("explicit ports unexpectedly have fallbacks: %v", cfg.FallbackListen)
	}
	for _, address := range []netip.Addr{
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("2001:db8::10"),
	} {
		if _, exists := cfg.IPWhitelist[address]; !exists {
			t.Fatalf("IP whitelist does not contain %s: %v", address, cfg.IPWhitelist)
		}
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
		"P2D_IP_WHITELIST",
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

func TestConfigUsesCompiledNetworkDefaults(t *testing.T) {
	t.Setenv("P2D_DOMAIN", "abc.dnslog.test")
	t.Setenv("P2D_PORTS", "")
	t.Setenv("P2D_XOR_KEY", "test-key")
	t.Setenv("P2D_AUTH_KEY", "authentication-test-key-32-bytes!")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNSServer != "223.5.5.5:53" {
		t.Fatalf("got DNS server %q, want compiled default", cfg.DNSServer)
	}
	for _, address := range []netip.Addr{
		netip.MustParseAddr("192.168.100.254"),
		netip.MustParseAddr("192.168.100.253"),
	} {
		if _, exists := cfg.IPWhitelist[address]; !exists {
			t.Fatalf("default IP whitelist does not contain %s: %v", address, cfg.IPWhitelist)
		}
	}
	if len(cfg.Listen) < minAutomaticPorts || len(cfg.Listen) > maxAutomaticPorts {
		t.Fatalf("got %d automatic listen addresses, want %d-%d", len(cfg.Listen), minAutomaticPorts, maxAutomaticPorts)
	}
	if len(cfg.Listen)+len(cfg.FallbackListen) != len(automaticPortPool) {
		t.Fatalf(
			"got %d automatic port candidates, want %d",
			len(cfg.Listen)+len(cfg.FallbackListen),
			len(automaticPortPool),
		)
	}
	allAddresses := append([]string(nil), cfg.Listen...)
	allAddresses = append(allAddresses, cfg.FallbackListen...)
	assertAutomaticAddresses(t, allAddresses)
}

func TestConfigAllowsExplicitlyEmptyIPWhitelist(t *testing.T) {
	t.Setenv("P2D_DOMAIN", "abc.dnslog.test")
	t.Setenv("P2D_IP_WHITELIST", "")
	t.Setenv("P2D_XOR_KEY", "test-key")
	t.Setenv("P2D_AUTH_KEY", "authentication-test-key-32-bytes!")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IPWhitelist) != 0 {
		t.Fatalf("got IP whitelist %v, want empty", cfg.IPWhitelist)
	}
}

func TestConfigRejectsInvalidIPWhitelist(t *testing.T) {
	t.Setenv("P2D_DOMAIN", "abc.dnslog.test")
	t.Setenv("P2D_IP_WHITELIST", "192.0.2.10,not-an-ip")
	t.Setenv("P2D_XOR_KEY", "test-key")
	t.Setenv("P2D_AUTH_KEY", "authentication-test-key-32-bytes!")

	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected invalid IP whitelist error")
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

func TestAutomaticPortPoolIsValidAndUnique(t *testing.T) {
	seen := make(map[int]struct{}, len(automaticPortPool))
	for _, port := range automaticPortPool {
		if port < 1 || port > 65535 {
			t.Fatalf("automatic port pool contains invalid port %d", port)
		}
		if _, exists := seen[port]; exists {
			t.Fatalf("automatic port pool contains duplicate port %d", port)
		}
		seen[port] = struct{}{}
	}
	if len(automaticPortPool) < maxAutomaticPorts {
		t.Fatalf("automatic port pool only contains %d ports", len(automaticPortPool))
	}
}

func assertAutomaticAddresses(t *testing.T, addresses []string) {
	t.Helper()

	allowed := make(map[int]struct{}, len(automaticPortPool))
	for _, port := range automaticPortPool {
		allowed[port] = struct{}{}
	}
	seen := make(map[int]struct{}, len(addresses))
	for _, address := range addresses {
		host, rawPort, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatalf("invalid automatic listen address %q: %v", address, err)
		}
		if host != defaultBindAddress {
			t.Fatalf("automatic listen address %q has host %q, want %q", address, host, defaultBindAddress)
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil {
			t.Fatalf("automatic listen address %q has invalid port: %v", address, err)
		}
		if _, exists := allowed[port]; !exists {
			t.Fatalf("automatic listen address %q is outside the port pool", address)
		}
		if _, exists := seen[port]; exists {
			t.Fatalf("automatic listen addresses contain duplicate port %d", port)
		}
		seen[port] = struct{}{}
	}
}
