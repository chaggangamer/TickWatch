# Changelog

All notable changes to Tickwatch are documented here.
The format is based on Keep a Changelog; versions follow semver.

## 0.1.0 - 2026-11-21

First release.

### Added

- HTTP, TCP, ping and heartbeat monitors with per-monitor interval, timeout,
  retries and failure threshold
- Incident state machine: down after N consecutive failures, up after one success
- Telegram, Slack, Discord, email (SMTP) and webhook alert channels with
  per-monitor selection, cooldown and test notifications
- SSL certificate expiry tracking with alerts at 30, 14 and 7 days
- SQLite storage with embedded migrations, WAL mode and foreign keys
- Retention: raw results kept 30 days (configurable), then aggregated into hourly
  summaries
- Uptime percentages for 24h, 7d, 30d and 90d
- Embedded dashboard: login with bcrypt and rate limiting, CSRF-protected forms,
  live monitor list with sparklines, response time chart drawn with plain SVG,
  incident history, channel management
- Public status page for monitors marked public
- Token-authenticated REST API for monitors, channels and incidents with a
  consistent error format
- CLI: serve, add, list, migrate, token add/list/delete, version, healthcheck
- Docker image (scratch based, non-root) and docker compose setup
- GitHub Actions CI: vet, staticcheck, tests with race detector, docker build
- goreleaser config for linux, darwin and windows on amd64 and arm64
