# Repository Guidelines

## Project Overview
SMAS (Server Monitoring and Alert System) - A Go-based daemon for monitoring server health, ports, processes, and resources with email/WeChat Work alerting.

## Project Structure
```
bin/                    # Go source files and compiled binaries
  *.go                  # Main application and feature modules
  go.mod                # Go module definition (go 1.24.12)
conf/
  config.json           # Runtime configuration (hot-reloadable)
templates/              # HTML email templates
log/                    # Runtime logs
bak/                    # Historical artifacts
```

## Build Commands

### Build the daemon (from repo root)
```bash
cd bin
go build -o smas_v2.5.2_x86 smas_v2.5.2.go monitor_func_v2.5.2.go email_v2.5.2.go webhook_v2.5.2.go report_summary_v2.5.2.go sjjs_monitor_v2.5.2.go configWatcher.go pushplus_v2.5.2.go
```

### Run locally
```bash
./smas_v2.5.2_x86
```

### Format code
```bash
gofmt -w bin/*.go
```

### Lint (if golint is installed)
```bash
golint ./...
```

## Test Commands

### Run all tests
```bash
cd bin
go test ./...
```

### Run a single test file
```bash
cd bin
go test -v -run TestFunctionName
```

### Run tests with coverage
```bash
cd bin
go test -cover ./...
```

## Code Style Guidelines

### Formatting
- Run `gofmt` before committing
- Use tabs for indentation (Go standard)
- Line length: keep under 120 characters when possible

### Imports
Order imports in three groups separated by blank lines:
1. Standard library packages
2. Third-party packages
3. Project internal packages (if any)

Example:
```go
import (
    "encoding/json"
    "fmt"
    "time"
    "github.com/fsnotify/fsnotify"
)
```

### Naming Conventions
- **Exported identifiers**: PascalCase (e.g., `ServerStatus`, `CheckPort`)
- **Unexported identifiers**: camelCase (e.g., `statusesMutex`, `parseAddresses`)
- **Structs**: PascalCase with descriptive names (e.g., `EmailTemplateData`)
- **Interfaces**: PascalCase with -er suffix (e.g., `Reader`, `Writer`)
- **Constants**: PascalCase or camelCase (e.g., `maxWorkers`, `checkCacheTTL`)
- **Versioned files**: Use pattern `*_v2.5.2.go` - update version consistently
- **JSON tags**: snake_case (e.g., `json:"cpu_threshold"`)

### Types
- Define configuration structs with JSON tags
- Use pointer types for optional boolean fields (`*bool`) to distinguish "not set" from "false"
- Prefer concrete types over `interface{}` when possible
- Use `sync.Map` for concurrent map access, or mutex-protected maps

### Error Handling
- Always check errors and handle them appropriately
- Return errors with context: `fmt.Errorf("operation failed: %w", err)`
- Log errors at the appropriate level before returning
- For critical initialization errors, use `log.Fatalf()`
- Example pattern:
```go
if err != nil {
    return fmt.Errorf("failed to parse addresses: %w", err)
}
```

### Comments
- Use Chinese or English consistently within a file
- Start with `//` for single-line comments
- Document exported functions, types, and packages
- Include version markers for significant changes: `// [v2.5.0新增]`

### Concurrency
- Use `sync.WaitGroup` for goroutine synchronization
- Use `sync.Mutex` or `sync.RWMutex` for shared state protection
- Use atomic operations (`atomic.Bool`, `atomic.Int32`) for simple flags
- Always defer mutex unlocks immediately after locking

### Configuration
- Define config structs with JSON tags
- Support hot-reload for configuration changes
- Keep credentials and sensitive URLs out of commits
- Validate JSON before deployment - config errors can crash the daemon

## Git Workflow

### Commit Messages
Use Conventional Commits format:
- `feat:` - New feature
- `fix:` - Bug fix
- `docs:` - Documentation changes
- `refactor:` - Code refactoring
- `perf:` - Performance improvements

Example: `feat: add ICMP ping support with configurable timeout`

### Pre-commit Checklist
- [ ] Code formatted with `gofmt`
- [ ] No hardcoded credentials or webhook URLs
- [ ] Config changes documented and validated
- [ ] Version numbers updated consistently across files

## Operational Notes
- Configuration changes via `conf/config.json` take effect via hot-reload
- The daemon monitors servers, ports, processes, and resources
- Alert channels: Email (SMTP), WeChat Work webhooks, and PushPlus
- Monitoring intervals and debounce behavior are critical - adjust carefully
- Keep credentials and webhook URLs out of version control

## Dependencies
- `github.com/fsnotify/fsnotify` - File system watching for config hot-reload
- Standard library only for core functionality
