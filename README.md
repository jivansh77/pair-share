# pair-share

Real-time terminal sharing for pair debugging and AI agent collaboration.

## Installation

```bash
go install github.com/jivansh77/pair-share/cmd/pair-share@latest
```

Or build from source:

```bash
git clone https://github.com/jivansh77/pair-share.git
cd pair-share
go build -o pair-share ./cmd/pair-share/
```

## Quick Start

**1. Start the relay server:**

```bash
pair-share serve
```

**2. In a new terminal, start a session:**

```bash
pair-share start --server ws://localhost:8080
```

You'll see output like:

```
✓ Session started
Session ID:   swift-koala-42
Join command: pair-share join swift-koala-42
Guests: 0 connected
```

**3. In another terminal, join the session:**

```bash
pair-share join swift-koala-42 --server ws://localhost:8080
```

The guest now sees the host's terminal in real-time and can type commands.

## Commands

### `pair-share start`

Start a new sharing session as the host.

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080` | Relay server URL |
| `--watch-only` | `false` | Guests join in watch-only mode |
| `--password` | | Password to protect the session |
| `--ttl` | `4h` | Session expiry duration |

### `pair-share join <session-id>`

Join an existing session as a guest.

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080` | Relay server URL |
| `--watch` | `false` | Join in watch-only mode |
| `--password` | | Session password if required |
| `--token` | | Agent access token |
| `--role` | | Connection role (`agent`) |

**Escape sequences (guest):** Press Enter then `~.` to disconnect, `~?` for help.

### `pair-share serve`

Run the WebSocket relay server.

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | Port to listen on |
| `--host` | `0.0.0.0` | Host to bind to |

### `pair-share checkpoint <label>`

Save a named checkpoint of the current session state.

```bash
pair-share checkpoint "before migration" --session swift-koala-42 --server ws://localhost:8080
```

Saves the terminal scrollback from the relay server and creates a `git stash` if inside a git repo. Checkpoints are stored locally at `~/.pair-share/checkpoints/<session-id>/`.

| Flag | Default | Description |
|------|---------|-------------|
| `--session` | *required* | Session ID |
| `--server` | `ws://localhost:8080` | Relay server URL |

### `pair-share rollback <label>`

Revert to a named checkpoint.

```bash
pair-share rollback "before migration" --session swift-koala-42
```

Displays the saved scrollback and prompts to restore the git stash.

| Flag | Default | Description |
|------|---------|-------------|
| `--session` | *required* | Session ID |
| `--no-git` | `false` | Skip git stash restore |

### `pair-share replay [session-id]`

Replay the activity log of a session. You can replay from a live relay server or from a saved log file.

**Replay from relay:**

```bash
pair-share replay swift-koala-42 --server ws://localhost:8080 --speed 2
```

**Export log for offline replay:**

```bash
pair-share replay swift-koala-42 --server ws://localhost:8080 --export demo-session.json
```

**Replay from saved file (works offline, after session expires):**

```bash
pair-share replay --from-file demo-session.json --speed 1.5
```

Plays back the session at real-time speed (or faster with `--speed`). Host output is shown in default color, guest input in white, and agent input in yellow.

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080` | Relay server URL |
| `--speed` | `1.0` | Playback speed multiplier |
| `--from-file` | | Replay from a saved log file instead of relay |
| `--export` | | Export the log to a file instead of replaying |

### `pair-share summon <agent>`

Grant an AI agent temporary access to the current session.

```bash
pair-share summon claude --session swift-koala-42 --access control --ttl 90s
```

Generates a one-time token and prints the join command for the agent:

```
✓ Agent access granted for 1m30s
  Agent:  claude
  Token:  tok_a1b2c3d4e5f6g7h8
  Access: control

  Run in agent:
    pair-share join swift-koala-42 --server ws://localhost:8080 --token tok_a1b2c3d4e5f6g7h8 --role agent
```

When the TTL expires, the agent is automatically disconnected.

| Flag | Default | Description |
|------|---------|-------------|
| `--session` | *required* | Session ID |
| `--server` | `ws://localhost:8080` | Relay server URL |
| `--access` | `watch` | Access level: `watch` or `control` |
| `--ttl` | `120s` | How long to grant access |

## Self-Hosting the Relay

The relay server is built into the same binary. For production use:

```bash
pair-share serve --port 8080 --host 0.0.0.0
```

Put it behind a reverse proxy (Caddy, nginx) for TLS:

```
relay.pairshare.dev {
    reverse_proxy localhost:8080
}
```

Clients then connect via:

```bash
pair-share start --server wss://relay.pairshare.dev
```

## Security Model

- **Transport encryption:** Use TLS via a reverse proxy for production. Local dev uses plain WebSocket.
- **Session passwords:** Optional password protection. Guests must provide the correct password.
- **Token-based agent access:** The `summon` command generates a one-time, time-limited token. Expired tokens are rejected and agent connections are force-closed.
- **Ephemeral sessions:** Sessions expire after the configured TTL (default 4h) and are purged from memory.
- **No persistence:** The relay server stores nothing to disk. All session state is in-memory.

## Architecture

```
┌──────────┐       WebSocket       ┌──────────────┐       WebSocket       ┌──────────┐
│   Host   │◄─────────────────────►│ Relay Server │◄─────────────────────►│  Guest   │
│  (PTY)   │   binary frames       │  (in-memory) │   binary frames       │ (raw tty)│
└──────────┘                       └──────────────┘                       └──────────┘
```

Messages use a binary framing protocol:
- `0x00` + bytes = raw PTY data (preserves ANSI escape codes)
- `0x01` + JSON = control messages (resize, role, info, checkpoint, summon)

## REST API

The relay server also exposes REST endpoints:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/session/:id` | GET | Session metadata |
| `/session/:id/log` | GET | Activity log (JSON) |
| `/session/:id/checkpoint` | POST | Get scrollback snapshot |
| `/session/:id/summon?access=...&ttl=...` | POST | Generate agent token |

## License

MIT
