package checker

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

func addrBlocked(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 100 && b[1] >= 64 && b[1] <= 127 {
			return true
		}
	}
	return false
}

func checkHost(host string, block bool) error {
	if !block {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if addrBlocked(ip) {
			return fmt.Errorf("target address %s is not allowed", ip)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed: %w", err)
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip)
		if ok && addrBlocked(a) {
			return fmt.Errorf("target resolves to a disallowed address")
		}
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %s has no addresses", host)
	}
	return nil
}

func newTransport(block bool, tlsCfg *tls.Config) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		if block {
			if err := checkHost(host, true); err != nil {
				return nil, err
			}
			ip, err := firstAllowed(host)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		}
		return dialer.DialContext(ctx, network, addr)
	}
	cfg := tlsCfg
	if cfg == nil {
		cfg = &tls.Config{}
	}
	return &http.Transport{
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       cfg,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

func firstAllowed(host string) (string, error) {
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("dns lookup failed: %w", err)
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip)
		if ok && !addrBlocked(a) {
			return a.String(), nil
		}
	}
	return "", fmt.Errorf("no allowed address for host %s", host)
}
