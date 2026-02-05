# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**SMAS (Server Monitoring & Alert System)** - A centralized Go-based monitoring daemon that continuously checks server health across multiple infrastructure nodes and sends alerts via email and WeChat Work.

Current version: **v2.5.0**

## Build & Run

```bash
# Build the monitoring system
cd bin/
go build -o smas_v2.5.0_x86 smas_v2.5.0.go monitor_func_v2.5.0.go \
  email_v2.5.0.go webhook_v2.5.0.go report_summary_v2.5.0.go \
  sjjs_monitor.go configWatcher.go

# Run directly
./smas_v2.5.0_x86

# Run with auto-restart wrapper
./smas_start.sh
```

**Dependencies:**
- `github.com/fsnotify/fsnotify` - Config hot-reload
- Standard library only (net, http, smtp, sync, time)

## Architecture

### Core Components

1. **Main Engine** (`smas_v2.5.0.go`)
   - Entry point with concurrent monitoring goroutines
   - Global state management for all servers
   - Signal handling for graceful shutdown

2. **Monitoring Functions** (`monitor_func_v2.5.0.go`)
   - `runPortChecks()` - TCP/UDP port connectivity
   - `runProcessChecks()` - Process status verification
   - `runResourceChecks()` - CPU/memory/disk usage
   - `runPingChecks()` - ICMP and TCP (port 22) ping
   - `runTargetPortChecks()` - Remote server port checks
   - `runDirectoryChecks()` - Directory/file existence

3. **Configuration** (`configWatcher.go`)
   - Hot-reload from `conf/config.json` using fsnotify
   - Thread-safe access with RWMutex
   - IP range expansion (e.g., `10.47.144.145-150`)

4. **Alert System**
   - Email (`email_v2.5.0.go`) - SMTP pooling, rate limiting, HTML templates
   - WeChat Work (`webhook_v2.5.0.go`) - Markdown messages with proxy support
   - Reports (`report_summary_v2.5.0.go`) - Daily/weekly summaries
   - Stream Reports (`sjjs_monitor.go`) - CDR statistics for XDR servers

5. **State Management**
   - `StateTracker` implements debouncing (consecutive failures before alerting)
   - Cooldown periods prevent alert spam
   - Distinguishes restart vs. persistent failure

### Alert Flow

```
Monitoring Function → StateTracker (debounce) → sendAlert() → Email/WeChat → Cache → Summary Reports
```

## Configuration Structure

`conf/config.json` contains:
- **Email/WeChat settings** - SMTP, webhooks, proxy configuration
- **Alert methods** - Enable/disable channels
- **Monitoring toggles** - Per-check type (process, port, ping, resource, directory)
- **Thresholds** - CPU, memory, disk percentages
- **Cooldown periods** - Failure, ping, port recovery windows
- **Ping options** - ICMP vs TCP (port 22)
- **Port-process mappings** - Group related ports/processes (v2.5.0)
- **Server groups** - Address ranges with per-server overrides
- **Report scheduling** - Summary and stream report times

**Server Configuration:**
- Address ranges expand: `"10.47.144.145-150"` → individual IPs
- Per-server thresholds override global defaults
- Per-server monitoring toggles
- Server types: `"XDR"` for stream report collection

## Key Features by Version

- **v2.5.0**: Port-process mapping, dual ping (ICMP+TCP), stream reports, cache persistence
- **v2.4.0**: Daily/weekly summary reports, severity-based email filtering
- **v2.3.0**: WeChat Work integration
- **v2.2.0**: Debouncing logic, restart detection
- **v2.0.0**: Email connection pooling, rate limiting

## Monitoring Intervals

- Port checks: 10s
- Process checks: 3s
- Resource checks: 90s
- Ping checks: 60s
- Directory checks: 5min (offset +2min)
- Target port checks: 30s

## Important Patterns

1. **Debouncing**: Requires `consecutiveToAlert` failures before triggering alerts
2. **Restart Detection**: Uses time windows to distinguish restart from persistent down
3. **Hot-Reload**: Config changes apply immediately without restart
4. **Connection Pooling**: HTTP and SMTP connections reused for performance
5. **Rate Limiting**: Email sending throttled to prevent spam
