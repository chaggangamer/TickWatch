package checker

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func (r *localRunner) runPing(ctx context.Context, m store.Monitor) Result {
	cfg, err := store.ParsePingConfig(m.Config)
	if err != nil {
		return fail(fmt.Sprintf("invalid config: %v", err))
	}
	if err := checkHost(cfg.Host, r.blockPrivate); err != nil {
		return fail(err.Error())
	}
	latency, pingErr, fallback := icmpPing(ctx, cfg.Host, timeoutOf(ctx))
	if !fallback {
		if pingErr != nil {
			return Result{OK: false, Error: pingErr.Error()}
		}
		return Result{OK: true, LatencyMS: latency}
	}
	return r.tcpFallback(ctx, cfg.Host)
}

func timeoutOf(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 10 * time.Second
	}
	d := time.Until(deadline)
	if d <= 0 {
		d = time.Second
	}
	return d
}

func icmpPing(ctx context.Context, host string, timeout time.Duration) (latency int64, err error, fallback bool) {
	network := "ip4:icmp"
	proto := 1
	addr, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		addr6, err6 := net.ResolveIPAddr("ip6", host)
		if err6 != nil {
			return 0, fmt.Errorf("dns lookup failed: %v", err), false
		}
		network = "ip6:icmp"
		proto = 58
		addr = addr6
	}

	conn, err := icmp.ListenPacket(network, "")
	if err != nil {
		return 0, err, true
	}
	defer conn.Close()

	id := rand.IntN(65535)
	var msgType icmp.Type = ipv4.ICMPTypeEcho
	if network == "ip6:icmp" {
		msgType = ipv6.ICMPTypeEchoRequest
	}
	body := &icmp.Echo{ID: id, Seq: 1, Data: []byte("tickwatch-probe")}
	msg := icmp.Message{Type: msgType, Code: 0, Body: body}
	out, err := msg.Marshal(nil)
	if err != nil {
		return 0, fmt.Errorf("build echo request: %v", err), false
	}

	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	conn.SetDeadline(deadline)

	start := time.Now()
	var target net.Addr = &net.IPAddr{IP: addr.IP}
	if _, err := conn.WriteTo(out, target); err != nil {
		return 0, fmt.Errorf("send echo request: %v", err), false
	}

	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0, fmt.Errorf("icmp socket closed"), false
			}
			return 0, fmt.Errorf("no icmp reply within %s", timeout), false
		}
		msg, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		var replyType icmp.Type = ipv4.ICMPTypeEchoReply
		if network == "ip6:icmp" {
			replyType = ipv6.ICMPTypeEchoReply
		}
		if msg.Type == replyType {
			if echo, ok := msg.Body.(*icmp.Echo); ok && echo.ID == id {
				return time.Since(start).Milliseconds(), nil, false
			}
		}
	}
}

func (r *localRunner) tcpFallback(ctx context.Context, host string) Result {
	var lastErr error
	for _, port := range r.fallbackPorts {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		start := time.Now()
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return Result{OK: true, LatencyMS: ms(time.Since(start))}
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return fail("request timed out")
	}
	return fail(fmt.Sprintf("host unreachable: icmp unavailable, tcp fallback failed: %v", describeError(lastErr)))
}
