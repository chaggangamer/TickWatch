package checker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tickwatch/tickwatch/internal/store"
)

type Result struct {
	OK           bool
	StatusCode   int
	LatencyMS    int64
	Error        string
	SSLExpiresAt int64
}

type Runner interface {
	Run(ctx context.Context, m store.Monitor) Result
}

type Options struct {
	BlockPrivate bool
	TLSConfig    *tls.Config
}

type localRunner struct {
	blockPrivate  bool
	clientFollow  *http.Client
	clientDirect  *http.Client
	fallbackPorts []int
}

func New(opts Options) Runner {
	tr := newTransport(opts.BlockPrivate, opts.TLSConfig)
	return &localRunner{
		blockPrivate:  opts.BlockPrivate,
		clientFollow:  &http.Client{Transport: tr},
		clientDirect:  &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }},
		fallbackPorts: []int{80, 443},
	}
}

func (r *localRunner) Run(ctx context.Context, m store.Monitor) Result {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
	}
	switch m.Type {
	case store.TypeHTTP:
		return r.runHTTP(ctx, m)
	case store.TypeTCP:
		return r.runTCP(ctx, m)
	case store.TypePing:
		return r.runPing(ctx, m)
	default:
		return fail(fmt.Sprintf("unknown monitor type %q", m.Type))
	}
}

func fail(msg string) Result {
	return Result{OK: false, Error: msg}
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

func describeError(err error) string {
	if err == nil {
		return "unknown error"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "request timed out"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Sprintf("dns lookup failed: %s", dnsErr.Err)
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "unknown error"
	}
	return msg
}

func sslExpiry(resp *http.Response) int64 {
	if resp == nil || resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return 0
	}
	return resp.TLS.PeerCertificates[0].NotAfter.Unix()
}
