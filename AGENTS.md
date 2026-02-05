# Repository Guidelines

## Project Structure & Module Organization
- `bin/` contains the Go sources and compiled binaries. The main entry point is `bin/smas_v2.5.0.go`, with feature modules such as `monitor_func_v2.5.0.go`, `email_v2.5.0.go`, and `configWatcher.go`.
- `conf/config.json` holds runtime configuration (alert channels, thresholds, server groups, schedules).
- `templates/` contains HTML templates for email alerts.
- `bak/` and `log/` are historical artifacts and runtime logs.

## Build, Test, and Development Commands
- Build the daemon (from repo root):
  - `cd bin`
  - `go build -o smas_v2.5.0_x86 smas_v2.5.0.go monitor_func_v2.5.0.go email_v2.5.0.go webhook_v2.5.0.go report_summary_v2.5.0.go sjjs_monitor.go configWatcher.go`
- Run locally: `./smas_v2.5.0_x86`
- Run with auto-restart wrapper (if present): `./smas_start.sh`

## Coding Style & Naming Conventions
- Use standard Go formatting via `gofmt` before committing.
- Follow Go naming conventions (PascalCase for exported, camelCase for unexported).
- Versioned files follow the pattern `*_v2.5.0.go`. Keep new versions consistent.
- Config keys should remain stable and documented in `conf/config.json`.

## Testing Guidelines
- No automated tests are currently in this repository.
- If adding tests, use `go test ./...` and name files `*_test.go`.

## Commit & Pull Request Guidelines
- Recent history uses Conventional Commits (e.g., `feat:`, `fix:`) with short, imperative summaries.
- PRs should include a clear description of behavior changes, config updates (if any), and any operational impact (e.g., new alerts or thresholds).

## Configuration & Operational Notes
- `conf/config.json` changes take effect via hot-reload. Validate JSON before deployment.
- Alert channels include email and WeChat Work; keep credentials and webhook URLs out of commits when possible.
- Monitoring intervals and debounce behavior are core to alert accuracy—adjust carefully and document changes.
