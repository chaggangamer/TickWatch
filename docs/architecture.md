# Architecture

This document explains how Tickwatch fits together and which seams exist for a
future hosted edition. It reflects the code as of 0.1.0.

## Overview

Tickwatch is one process. `cmd/tickwatch` loads config, runs migrations, wires the
packages below and starts an HTTP server plus the check engine. All state lives in
one SQLite file. Nothing else runs: no queue, no cache server, no JS build step.

```
cmd/tickwatch          wiring, CLI subcommands, graceful shutdown
internal/config        YAML + env, defaults, validation
internal/store         Store interface, SQLite implementation, migrations, retention
internal/checker       http/tcp/ping runners, SSRF guard, Runner interface
internal/incident      state machine: fail counters, down/up transitions
internal/alert         dispatcher, channel senders, cooldown, masking
internal/scheduler     worker pool, due scans, jitter, heartbeat sweeper, ssl sweep
internal/api           chi router, token auth, JSON CRUD, healthz, ping
internal/web           embedded dashboard: sessions, CSRF, templates, static assets
```

## Check flow

1. The scheduler queries due monitors every second
   (`next_run_at <= now`, plus a startup stagger so checks do not align on boot).
2. Due monitors are pushed into a bounded channel consumed by a fixed worker pool.
3. A worker runs the check with a per-attempt context derived from the monitor
   timeout and retry count, with jitter added to the next run time.
4. The result is stored, the incident tracker decides whether the state changed,
   and state transitions are handed to the alert dispatcher as events.
5. Heartbeat monitors skip the check loop entirely. A sweeper marks monitors down
   when `next_deadline_at` passes; `HandlePing` resets the deadline and recovers.

## Data flow and retention

Every check appends a row to `check_results`. An hourly job aggregates rows older
than `retention.raw_days` into `hourly_summaries` (counts and latency sums per
monitor and hour bucket) and deletes the raw rows. Uptime percentages read raw
results plus summaries, so windows longer than raw retention still resolve.
Incidents and alert deduplication state stay full-term.

## Concurrency model

- One bounded jobs channel; workers own check execution.
- The incident tracker guards per-monitor counters with a mutex; it is the only
  shared mutable state besides the store.
- The alert dispatcher runs a single consumer goroutine fed by a buffered channel;
  reminders come from its own ticker. Submit never blocks.
- All loops select on context cancellation; `Run` waits on a WaitGroup so shutdown
  waits for in-flight checks, and the HTTP server shuts down with its own timeout.

## Security boundaries

- Dashboard sessions are random tokens stored hashed; cookies are HttpOnly,
  SameSite Lax, and Secure when base_url is https.
- Every dashboard POST validates a per-session CSRF token in constant time.
- Login attempts are rate limited per IP (5 per minute, in memory).
- The API accepts bearer tokens stored as sha256 hashes; `last_used_at` is
  throttled to avoid write amplification.
- Channel secrets are masked in every API and dashboard response; masked values
  resubmitted mean "keep the stored secret".
- SSRF protection resolves target hosts and rejects loopback, private, link-local,
  multicast, unspecified and CGNAT ranges when `ssrf.block_private` is enabled;
  the HTTP transport dials the validated IP while keeping hostname SNI.
- All SQL is parameterised.

## Open-core seams

Kept deliberately narrow so a hosted edition can plug in without rewriting the
core:

| Seam | Interface | Hosted extension |
| ---- | --------- | ---------------- |
| Storage | `store.Store` | Postgres or another backend implementing the same interface |
| Check execution | `checker.Runner` | Remote region runners instead of the local runner |
| Users | `store.Store` user methods | Multi-user, teams, SSO on top of the same shape |
| Alerts | `alert.Dispatcher` senders switch | Additional providers, per-tenant routing |
| Billing | not built | Hooks would hang off monitor/channel ownership once teams exist |

Nothing in core depends on the assumption of exactly one user or one region; the
single-admin bootstrap in `cmd/tickwatch/serve.go` is the only place that does.

## Layout of the database

See `internal/store/migrations/0001_init.sql`. Timestamps are unix seconds.
`monitors.config` and `channels.config` hold per-type JSON validated in Go before
storage. Foreign keys cascade on monitor and channel deletion.
