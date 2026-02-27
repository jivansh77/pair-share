package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/fatih/color"
	"github.com/gorilla/websocket"
	"golang.org/x/term"

	pairpty "github.com/jivansh77/pair-share/internal/pty"
	"github.com/jivansh77/pair-share/internal/relay"
	"github.com/jivansh77/pair-share/internal/session"
)

var (
	infoStyle    = color.New(color.FgHiBlack)
	sessionStyle = color.New(color.FgCyan, color.Bold)
	successStyle = color.New(color.FgGreen)
	errorStyle   = color.New(color.FgRed)
)

type HostOptions struct {
	ServerURL string
	SessionID string
	WatchOnly bool
	Password  string
	TTL       string
}

type GuestOptions struct {
	ServerURL string
	SessionID string
	Watch     bool
	Password  string
}

// RunHost starts a shared terminal session as the host.
func RunHost(opts HostOptions) error {
	u, err := buildURL(opts.ServerURL, map[string]string{
		"session":    opts.SessionID,
		"role":       "host",
		"ttl":        opts.TTL,
		"watch_only": boolStr(opts.WatchOnly),
		"password":   opts.Password,
	})
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}

	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		errorStyle.Fprintf(os.Stderr, "Failed to connect to relay server: %v\n", err)
		fmt.Fprintln(os.Stderr, "Tip: make sure the relay is running (pair-share serve)")
		return err
	}
	defer conn.Close()

	ptty, err := pairpty.Start()
	if err != nil {
		return fmt.Errorf("failed to start shell: %w", err)
	}
	defer ptty.Close()

	// Send initial terminal size
	if cols, rows, err := pairpty.GetTerminalSize(); err == nil {
		sendControl(conn, relay.ControlMessage{Type: "resize", Cols: cols, Rows: rows})
	}

	successStyle.Println("✓ Session started")
	fmt.Print("Session ID:   ")
	sessionStyle.Println(opts.SessionID)
	fmt.Printf("Join command: pair-share join %s\n", opts.SessionID)
	infoStyle.Println("Guests: 0 connected")
	fmt.Println()

	// Put local terminal into raw mode so keystrokes pass through
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("failed to set raw terminal: %w", err)
	}
	defer term.Restore(fd, oldState)

	// Catch signals to restore terminal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		term.Restore(fd, oldState)
		os.Exit(0)
	}()

	done := make(chan struct{})
	stop := make(chan struct{})

	// Watch for terminal resize
	go pairpty.WatchResize(func(size session.TermSize) {
		ptty.Resize(size.Cols, size.Rows)
		sendControl(conn, relay.ControlMessage{Type: "resize", Cols: size.Cols, Rows: size.Rows})
	}, stop)

	// PTY → WebSocket (host output to guests)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptty.File.Read(buf)
			if err != nil {
				close(done)
				return
			}
			// Write to local terminal
			os.Stdout.Write(buf[:n])
			// Send to relay
			frame := relay.MakeDataFrame(buf[:n])
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				close(done)
				return
			}
		}
	}()

	// Local stdin → PTY
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			ptty.File.Write(buf[:n])
		}
	}()

	// WebSocket → PTY (guest keystrokes to host shell)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				close(done)
				return
			}
			if len(data) == 0 {
				continue
			}
			msgType, payload := relay.ParseFrame(data)
			switch msgType {
			case relay.MsgTypePTY:
				// Guest keystrokes → write to PTY
				ptty.File.Write(payload)
			case relay.MsgTypeControl:
				handleHostControlMsg(payload, fd, oldState)
			}
		}
	}()

	<-done
	close(stop)
	return nil
}

// RunGuest joins an existing session as a guest.
func RunGuest(opts GuestOptions) error {
	params := map[string]string{
		"session":  opts.SessionID,
		"role":     "guest",
		"password": opts.Password,
	}
	if opts.Watch {
		params["watch"] = "true"
	}

	u, err := buildURL(opts.ServerURL, params)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}

	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		errorStyle.Fprintf(os.Stderr, "Failed to connect to relay server: %v\n", err)
		fmt.Fprintln(os.Stderr, "Tip: make sure the relay is running (pair-share serve)")
		return err
	}
	defer conn.Close()

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("failed to set raw terminal: %w", err)
	}
	defer term.Restore(fd, oldState)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		term.Restore(fd, oldState)
		infoStyle.Fprintln(os.Stderr, "\n[pair-share] Disconnected.")
		os.Exit(0)
	}()

	// Show connection hint
	infoStyle.Fprintln(os.Stderr, "[pair-share] Connected. Press Enter, then ~. to disconnect (~? for help)")

	done := make(chan struct{})
	disconnect := make(chan struct{})

	// WebSocket → stdout (see host's terminal)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				close(done)
				return
			}
			if len(data) == 0 {
				continue
			}
			msgType, payload := relay.ParseFrame(data)
			switch msgType {
			case relay.MsgTypePTY:
				os.Stdout.Write(payload)
			case relay.MsgTypeControl:
				if handleGuestControlMsg(payload, fd, oldState) {
					close(done)
					return
				}
			}
		}
	}()

	// stdin → WebSocket (send keystrokes to host)
	// Supports SSH-style escape sequence: ~. to disconnect
	go func() {
		buf := make([]byte, 256)
		afterNewline := true // Start as if after newline
		escapePending := false

		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if err != io.EOF {
					close(done)
				}
				return
			}

			// Process each byte for escape sequence detection
			toSend := make([]byte, 0, n)
			for i := 0; i < n; i++ {
				b := buf[i]

				if escapePending {
					escapePending = false
					switch b {
					case '.':
						// ~. = disconnect
						term.Restore(fd, oldState)
						infoStyle.Fprintln(os.Stderr, "\n[pair-share] Disconnected.")
						close(disconnect)
						return
					case '~':
						// ~~ = send single ~
						toSend = append(toSend, '~')
					case '?':
						// ~? = show help (don't send)
						showEscapeHelp(fd, oldState)
					default:
						// Not a recognized escape, send ~ and the char
						toSend = append(toSend, '~', b)
					}
					afterNewline = (b == '\r' || b == '\n')
					continue
				}

				if afterNewline && b == '~' {
					// Start of potential escape sequence
					escapePending = true
					continue
				}

				afterNewline = (b == '\r' || b == '\n')
				toSend = append(toSend, b)
			}

			if len(toSend) > 0 {
				frame := relay.MakeDataFrame(toSend)
				if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					return
				}
			}
		}
	}()

	select {
	case <-done:
	case <-disconnect:
	}
	return nil
}

func showEscapeHelp(fd int, oldState *term.State) {
	help := "\r\n[pair-share] Escape sequences:\r\n" +
		"  ~.  - Disconnect from session\r\n" +
		"  ~~  - Send literal ~\r\n" +
		"  ~?  - Show this help\r\n"
	os.Stderr.WriteString(help)
}

func handleHostControlMsg(jsonData []byte, fd int, oldState *term.State) {
	var msg relay.ControlMessage
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		return
	}

	switch msg.Type {
	case "info":
		role := msg.Role
		if role == "" {
			// Guest left (no role in leave notification)
			writeNotification(fd, oldState, fmt.Sprintf("[pair-share] Guests: %d connected", msg.Guests))
		} else {
			writeNotification(fd, oldState, fmt.Sprintf("[pair-share] 👤 Guest joined (%s) — %d connected", role, msg.Guests))
		}
	}
}

// handleGuestControlMsg returns true if the session should end.
func handleGuestControlMsg(jsonData []byte, fd int, oldState *term.State) bool {
	var msg relay.ControlMessage
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		return false
	}

	switch msg.Type {
	case "resize":
		// Nothing to do in terminal—the output stream will handle it
	case "role":
		writeNotification(fd, oldState, fmt.Sprintf("[pair-share] Role: %s", msg.Role))
	case "error":
		term.Restore(fd, oldState)
		errorStyle.Fprintf(os.Stderr, "\n%s\n", msg.Error)
		return true
	}
	return false
}

// writeNotification temporarily restores the terminal to print a status line,
// then re-enters raw mode. Uses save/restore cursor to avoid disrupting output.
func writeNotification(fd int, oldState *term.State, text string) {
	// Save cursor, move to top, print, restore cursor
	formatted := fmt.Sprintf("\033[s\033[1;1H\033[2K%s\033[u", text)
	os.Stdout.WriteString(formatted)
}

func sendControl(conn *websocket.Conn, msg relay.ControlMessage) {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return
	}
	frame := make([]byte, 1+len(jsonData))
	frame[0] = relay.MsgTypeControl
	copy(frame[1:], jsonData)
	_ = conn.WriteMessage(websocket.BinaryMessage, frame)
}

func buildURL(serverURL string, params map[string]string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}

	// Convert http(s) to ws(s) if needed
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}

	u.Path = "/ws"
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return ""
}
