package client

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"portlistener2dns/client/internal/protocol"
)

const (
	defaultPorts       = "139,445,1080,1099"
	defaultBindAddress = "0.0.0.0"
	defaultMarker      = "listener-req"
	defaultQueueSize   = 256
	defaultWorkers     = 2
	defaultRetries     = 3
	defaultQueryTimout = 3 * time.Second
)

type Config struct {
	Domain      string
	DNSServer   string
	XORKey      []byte
	AuthKey     []byte
	Listen      []string
	Marker      string
	QueueSize   int
	Workers     int
	Retries     int
	QueryTimout time.Duration
	Verbose     bool
}

// ConfigFromEnv reads all configuration once and then removes it from the
// process environment. This limits accidental disclosure through /proc and
// child processes; it does not replace proper permissions on an environment
// file or service manager.
func ConfigFromEnv() (Config, error) {
	values := map[string]string{}
	names := []string{
		"P2D_DOMAIN",
		"P2D_DNS_SERVER",
		"P2D_XOR_KEY",
		"P2D_AUTH_KEY",
		"P2D_PORTS",
		"P2D_BIND_ADDRESS",
		"P2D_MARKER",
		"P2D_QUEUE_SIZE",
		"P2D_WORKERS",
		"P2D_RETRIES",
		"P2D_QUERY_TIMEOUT",
		"P2D_VERBOSE",
	}
	for _, name := range names {
		values[name] = os.Getenv(name)
		_ = os.Unsetenv(name)
	}

	cfg := Config{
		Domain:      strings.Trim(strings.TrimSpace(values["P2D_DOMAIN"]), "."),
		XORKey:      []byte(values["P2D_XOR_KEY"]),
		AuthKey:     []byte(values["P2D_AUTH_KEY"]),
		Marker:      valueOrDefault(values["P2D_MARKER"], defaultMarker),
		QueueSize:   defaultQueueSize,
		Workers:     defaultWorkers,
		Retries:     defaultRetries,
		QueryTimout: defaultQueryTimout,
	}

	if cfg.Domain == "" {
		return Config{}, errors.New("P2D_DOMAIN 不能为空")
	}
	if len(cfg.XORKey) < 8 {
		return Config{}, errors.New("P2D_XOR_KEY 至少需要 8 个字节")
	}
	if len(cfg.AuthKey) < 32 {
		return Config{}, errors.New("P2D_AUTH_KEY 至少需要 32 个字节")
	}

	var err error
	cfg.DNSServer, err = normalizeDNSServer(values["P2D_DNS_SERVER"])
	if err != nil {
		return Config{}, fmt.Errorf("P2D_DNS_SERVER: %w", err)
	}

	cfg.Listen, err = parseListenAddresses(
		valueOrDefault(values["P2D_BIND_ADDRESS"], defaultBindAddress),
		valueOrDefault(values["P2D_PORTS"], defaultPorts),
	)
	if err != nil {
		return Config{}, err
	}

	if cfg.QueueSize, err = parsePositiveInt(values["P2D_QUEUE_SIZE"], defaultQueueSize); err != nil {
		return Config{}, fmt.Errorf("P2D_QUEUE_SIZE: %w", err)
	}
	if cfg.Workers, err = parsePositiveInt(values["P2D_WORKERS"], defaultWorkers); err != nil {
		return Config{}, fmt.Errorf("P2D_WORKERS: %w", err)
	}
	if cfg.Retries, err = parsePositiveInt(values["P2D_RETRIES"], defaultRetries); err != nil {
		return Config{}, fmt.Errorf("P2D_RETRIES: %w", err)
	}
	if values["P2D_QUERY_TIMEOUT"] != "" {
		cfg.QueryTimout, err = time.ParseDuration(values["P2D_QUERY_TIMEOUT"])
		if err != nil || cfg.QueryTimout <= 0 {
			return Config{}, errors.New("必须是正数时长，例如 3s")
		}
	}
	if values["P2D_VERBOSE"] != "" {
		cfg.Verbose, err = strconv.ParseBool(strings.TrimSpace(values["P2D_VERBOSE"]))
		if err != nil {
			return Config{}, errors.New("P2D_VERBOSE 必须是 true 或 false")
		}
	}
	if err := protocol.ValidateNamespace(cfg.Marker, cfg.Domain); err != nil {
		return Config{}, fmt.Errorf("DNS 命名空间无效: %w", err)
	}
	return cfg, nil
}

func parseListenAddresses(bindAddress, portsValue string) ([]string, error) {
	bindAddress = strings.Trim(strings.TrimSpace(bindAddress), "[]")
	if net.ParseIP(bindAddress) == nil {
		return nil, errors.New("P2D_BIND_ADDRESS 必须是 IPv4 或 IPv6 地址")
	}

	seen := make(map[int]struct{})
	addresses := make([]string, 0)
	for _, rawPort := range strings.Split(portsValue, ",") {
		rawPort = strings.TrimSpace(rawPort)
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("P2D_PORTS 包含无效端口 %q", rawPort)
		}
		if _, exists := seen[port]; exists {
			return nil, fmt.Errorf("P2D_PORTS 包含重复端口 %d", port)
		}
		seen[port] = struct{}{}
		addresses = append(addresses, net.JoinHostPort(bindAddress, strconv.Itoa(port)))
	}
	if len(addresses) == 0 {
		return nil, errors.New("P2D_PORTS 不能为空")
	}
	return addresses, nil
}

func valueOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func parsePositiveInt(value string, fallback int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("必须是正整数")
	}
	return parsed, nil
}

func normalizeDNSServer(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("不能为空，必须指定内网 DNS 服务器的 IP")
	}

	if ip := net.ParseIP(value); ip != nil {
		return net.JoinHostPort(value, "53"), nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", errors.New("应为 IP 或 IP:端口")
	}
	if net.ParseIP(host) == nil {
		return "", errors.New("必须使用 IP 地址，不能使用主机名")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("端口必须在 1 到 65535 之间")
	}
	return net.JoinHostPort(host, port), nil
}
