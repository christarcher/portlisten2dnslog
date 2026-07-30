package client

import "time"

const (
	defaultDNSServer    = "223.5.5.5"
	defaultIPWhitelist  = "192.168.100.254,192.168.100.253"
	defaultPorts        = "139,445,1080,1099"
	defaultBindAddress  = "0.0.0.0"
	defaultMarker       = "listener-req"
	defaultQueueSize    = 256
	defaultWorkers      = 2
	defaultRetries      = 3
	defaultQueryTimeout = 3 * time.Second
)
