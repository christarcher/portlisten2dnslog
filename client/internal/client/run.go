package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"portlistener2dns/client/internal/protocol"
)

func Run(ctx context.Context, cfg Config, logger *log.Logger) error {
	listeners := make([]net.Listener, 0, len(cfg.Listen))
	for _, address := range cfg.Listen {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
		if err != nil {
			closeListeners(listeners)
			return fmt.Errorf("监听 %s: %w", address, err)
		}
		listeners = append(listeners, listener)
		logger.Printf("正在监听 TCP %s", listener.Addr())
	}

	events := make(chan protocol.Event, cfg.QueueSize)
	var workerGroup sync.WaitGroup
	for i := 0; i < cfg.Workers; i++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			runWorker(ctx, cfg, events, logger)
		}()
	}

	var acceptGroup sync.WaitGroup
	for _, listener := range listeners {
		acceptGroup.Add(1)
		go func(value net.Listener) {
			defer acceptGroup.Done()
			acceptLoop(ctx, value, cfg.IPWhitelist, events, logger)
		}(listener)
	}

	<-ctx.Done()
	closeListeners(listeners)
	acceptGroup.Wait()
	close(events)
	workerGroup.Wait()
	logger.Print("已停止")
	return nil
}

func acceptLoop(
	ctx context.Context,
	listener net.Listener,
	ipWhitelist map[netip.Addr]struct{},
	events chan<- protocol.Event,
	logger *log.Logger,
) {
	var retryDelay time.Duration
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			if retryDelay == 0 {
				retryDelay = 5 * time.Millisecond
			} else {
				retryDelay *= 2
			}
			if retryDelay > time.Second {
				retryDelay = time.Second
			}
			logger.Printf("接受 %s 的连接失败，将重试: %v", listener.Addr(), err)
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		retryDelay = 0

		remote, remoteOK := connection.RemoteAddr().(*net.TCPAddr)
		local, localOK := connection.LocalAddr().(*net.TCPAddr)
		_ = connection.Close()
		if !remoteOK || !localOK {
			logger.Printf("忽略无法识别端点的连接: %s -> %s", connection.RemoteAddr(), connection.LocalAddr())
			continue
		}

		sourceIP := addrFromIP(remote.IP)
		targetIP := addrFromIP(local.IP)
		if !sourceIP.IsValid() || !targetIP.IsValid() {
			logger.Printf("忽略 IP 地址无效的连接: %s -> %s", remote, local)
			continue
		}
		if _, whitelisted := ipWhitelist[sourceIP]; whitelisted {
			logger.Printf("忽略白名单来源 IP 的连接: %s -> %s", remote, local)
			continue
		}

		id, err := protocol.NewUUID()
		if err != nil {
			logger.Printf("无法生成事件 UUID: %v", err)
			continue
		}
		event := protocol.Event{
			ID:         id,
			SourceIP:   sourceIP,
			SourcePort: uint16(remote.Port),
			TargetIP:   targetIP,
			TargetPort: uint16(local.Port),
			Time:       time.Now().UTC().Truncate(time.Second),
		}

		select {
		case events <- event:
		default:
			logger.Printf("告警队列已满，丢弃事件 %s (%s -> %s)", event.ID, remote, local)
		}
	}
}

func runWorker(ctx context.Context, cfg Config, events <-chan protocol.Event, logger *log.Logger) {
	for event := range events {
		payload, err := protocol.Encode(event, cfg.XORKey, cfg.AuthKey)
		if err != nil {
			logger.Printf("编码事件 %s 失败: %v", event.ID, err)
			continue
		}
		qname, err := protocol.BuildQName(event.ID, payload, cfg.Marker, cfg.Domain)
		if err != nil {
			logger.Printf("构造事件 %s 的 DNS 名称失败: %v", event.ID, err)
			continue
		}
		if err := sendWithRetry(ctx, cfg.DNSServer, qname, cfg.Retries, cfg.QueryTimout); err != nil {
			logger.Printf("发送事件 %s 失败: %v", event.ID, err)
			continue
		}
		logger.Printf(
			"已发送事件 %s: %s:%d -> %s:%d",
			event.ID,
			event.SourceIP,
			event.SourcePort,
			event.TargetIP,
			event.TargetPort,
		)
	}
}

func addrFromIP(value net.IP) netip.Addr {
	addr, ok := netip.AddrFromSlice(value)
	if !ok {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func closeListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}
