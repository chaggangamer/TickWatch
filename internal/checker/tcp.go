package checker

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

func (r *localRunner) runTCP(ctx context.Context, m store.Monitor) Result {
	cfg, err := store.ParseTCPConfig(m.Config)
	if err != nil {
		return fail(fmt.Sprintf("invalid config: %v", err))
	}
	if err := checkHost(cfg.Host, r.blockPrivate); err != nil {
		return fail(err.Error())
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	latency := time.Since(start)
	if err != nil {
		return Result{OK: false, LatencyMS: ms(latency), Error: describeError(err)}
	}
	conn.Close()
	return Result{OK: true, LatencyMS: ms(latency)}
}
