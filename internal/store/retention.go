package store

import (
	"context"
	"log/slog"
	"time"
)

func RunRetention(ctx context.Context, s Store, rawDays, summaryDays int, log *slog.Logger) {
	run := func() {
		now := time.Now().Unix()
		cutoff := now - int64(rawDays)*86400
		n, err := s.AggregateResults(cutoff)
		if err != nil {
			log.Error("aggregate results failed", "err", err)
		} else if n > 0 {
			log.Info("aggregated raw results into hourly summaries", "deleted", n, "older_than_days", rawDays)
		}
		m, err := s.DeleteSummaries(now - int64(summaryDays)*86400)
		if err != nil {
			log.Error("delete old summaries failed", "err", err)
		} else if m > 0 {
			log.Info("deleted expired summaries", "count", m)
		}
		exp, err := s.DeleteExpiredSessions(now)
		if err != nil {
			log.Error("delete expired sessions failed", "err", err)
		} else if exp > 0 {
			log.Info("deleted expired sessions", "count", exp)
		}
	}

	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			run()
			timer.Reset(time.Hour)
		}
	}
}
