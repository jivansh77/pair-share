## Cursor Cloud specific instructions

**pair-share** is a single-binary Go CLI tool for real-time terminal sharing. See `README.md` for full usage.

### Key services

| Service | Command | Notes |
|---|---|---|
| Relay server | `./pair-share serve --port 8080` | Must be running first; all other commands connect to it |
| Host session | `./pair-share start --server ws://localhost:8080` | Requires a PTY; use `script -qc "..." /dev/null` when running non-interactively |
| Guest join | `./pair-share join <session-id> --server ws://localhost:8080` | Also requires a PTY for terminal rendering |

### Build, lint, and test

- **Build:** `go build -o pair-share ./cmd/pair-share/`
- **Lint:** `go vet ./...`
- **Tests:** No `*_test.go` files exist in this codebase; there is no automated test suite to run.
- **REST health check:** `curl http://localhost:8080/health` (returns `{"status":"ok"}` when relay is up)

### Gotchas

- The `start` and `join` commands allocate a PTY and enter raw terminal mode. When running from a non-interactive shell (e.g., background processes), wrap with `script -qc "..." /dev/null` to supply a pseudo-TTY.
- The relay server is purely in-memory — no database or external service dependencies.
- Go 1.25.0 is specified in `go.mod`; the Go toolchain auto-downloads it if a newer `gotoolchain` is available.
