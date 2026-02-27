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

### `pair-share serve`

Run the WebSocket relay server.

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | Port to listen on |
| `--host` | `0.0.0.0` | Host to bind to |

## Self-Hosting the Relay

The relay server is built into the same binary. For production use:

```bash
# Generate TLS certs (e.g., with Let's Encrypt)
# Then run behind a reverse proxy (nginx, caddy) that terminates TLS

pair-share serve --port 8080 --host 0.0.0.0
```

Example Caddy config:

```
relay.pairshare.dev {
    reverse_proxy localhost:8080
}
```

Clients connect via:

```bash
pair-share start --server wss://relay.pairshare.dev
```

## Security Model

- **Transport encryption:** Use TLS via a reverse proxy (nginx, Caddy) for production deployments. Local dev uses plain WebSocket.
- **Session passwords:** Optional password protection for sessions. Guests must provide the password to join.
- **Ephemeral sessions:** Sessions expire after the configured TTL (default 4h) and are purged from memory.
- **No persistence:** The relay server stores nothing to disk. All session state is in-memory and lost on restart.

## Architecture

```
┌──────────┐       WebSocket       ┌──────────────┐       WebSocket       ┌──────────┐
│   Host   │◄─────────────────────►│ Relay Server │◄─────────────────────►│  Guest   │
│  (PTY)   │   binary frames       │  (in-memory) │   binary frames       │ (raw tty)│
└──────────┘                       └──────────────┘                       └──────────┘
```

Messages use a simple binary framing protocol:
- `0x00` + bytes = raw PTY data (preserves ANSI escape codes)
- `0x01` + JSON = control messages (resize, role, info)

## License

MIT
