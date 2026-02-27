package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/jivansh77/pair-share/internal/checkpoint"
	"github.com/jivansh77/pair-share/internal/client"
	actlog "github.com/jivansh77/pair-share/internal/log"
	"github.com/jivansh77/pair-share/internal/relay"
	"github.com/jivansh77/pair-share/internal/session"
)

var defaultServer = "ws://localhost:8080"

var (
	successStyle = color.New(color.FgGreen)
	errorStyle   = color.New(color.FgRed)
	sessionStyle = color.New(color.FgCyan, color.Bold)
	infoStyle    = color.New(color.FgHiBlack)
	agentStyle   = color.New(color.FgYellow)
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "pair-share",
		Short: "Real-time terminal sharing for pair debugging",
		Long:  "A lightweight CLI tool for secure, real-time terminal sharing, purpose-built for pair debugging and AI agent collaboration.",
	}

	rootCmd.AddCommand(startCmd())
	rootCmd.AddCommand(joinCmd())
	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(checkpointCmd())
	rootCmd.AddCommand(rollbackCmd())
	rootCmd.AddCommand(replayCmd())
	rootCmd.AddCommand(summonCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func startCmd() *cobra.Command {
	var server string
	var watchOnly bool
	var password string
	var ttl string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a new terminal sharing session",
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, err := session.GenerateID()
			if err != nil {
				return fmt.Errorf("failed to generate session ID: %w", err)
			}

			return client.RunHost(client.HostOptions{
				ServerURL: server,
				SessionID: sessionID,
				WatchOnly: watchOnly,
				Password:  password,
				TTL:       ttl,
			})
		},
	}

	cmd.Flags().StringVar(&server, "server", defaultServer, "Relay server URL")
	cmd.Flags().BoolVar(&watchOnly, "watch-only", false, "Guests join in watch-only mode by default")
	cmd.Flags().StringVar(&password, "password", "", "Password to protect the session")
	cmd.Flags().StringVar(&ttl, "ttl", "4h", "Session expiry duration")

	return cmd
}

func joinCmd() *cobra.Command {
	var server string
	var watch bool
	var password string
	var token string
	var role string

	cmd := &cobra.Command{
		Use:   "join <session-id>",
		Short: "Join an existing terminal sharing session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return client.RunGuest(client.GuestOptions{
				ServerURL: server,
				SessionID: args[0],
				Watch:     watch,
				Password:  password,
				Token:     token,
				Role:      role,
			})
		},
	}

	cmd.Flags().StringVar(&server, "server", defaultServer, "Relay server URL")
	cmd.Flags().BoolVar(&watch, "watch", false, "Join in watch-only mode")
	cmd.Flags().StringVar(&password, "password", "", "Session password")
	cmd.Flags().StringVar(&token, "token", "", "Agent access token")
	cmd.Flags().StringVar(&role, "role", "", "Connection role (agent)")

	return cmd
}

func serveCmd() *cobra.Command {
	var port int
	var host string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the relay server",
		RunE: func(cmd *cobra.Command, args []string) error {
			banner := color.New(color.FgCyan, color.Bold)
			banner.Println("pair-share relay server")
			fmt.Println()

			srv := relay.NewServer()
			addr := relay.FormatServerAddr(host, port)
			return srv.Start(addr)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
	cmd.Flags().StringVar(&host, "host", "0.0.0.0", "Host to bind to")

	return cmd
}

func checkpointCmd() *cobra.Command {
	var server string
	var sessionID string

	cmd := &cobra.Command{
		Use:   "checkpoint <label>",
		Short: "Save a named checkpoint of the current session state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]

			// Fetch scrollback from relay
			httpURL := wsToHTTP(server)
			resp, err := http.Post(
				fmt.Sprintf("%s/session/%s/checkpoint", httpURL, sessionID),
				"application/json",
				nil,
			)
			if err != nil {
				return fmt.Errorf("failed to contact relay: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("relay error: %s", strings.TrimSpace(string(body)))
			}

			var result struct {
				ScrollbackB64 string `json:"scrollback_b64"`
				ScrollbackLen int    `json:"scrollback_len"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			scrollback, _ := base64.StdEncoding.DecodeString(result.ScrollbackB64)

			chk, err := checkpoint.Save(sessionID, label, scrollback)
			if err != nil {
				return err
			}

			successStyle.Printf("✓ Checkpoint saved: %s %q\n", chk.ID, chk.Label)
			if chk.GitStash != "" {
				fmt.Printf("  Git stash: %s\n", chk.GitStash)
			}
			fmt.Printf("  Scrollback: %d bytes saved\n", len(scrollback))
			return nil
		},
	}

	cmd.Flags().StringVar(&server, "server", defaultServer, "Relay server URL")
	cmd.Flags().StringVar(&sessionID, "session", "", "Session ID (required)")
	_ = cmd.MarkFlagRequired("session")

	return cmd
}

func rollbackCmd() *cobra.Command {
	var sessionID string
	var noGit bool

	cmd := &cobra.Command{
		Use:   "rollback <label>",
		Short: "Revert to a named checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]

			chk, err := checkpoint.Load(sessionID, label)
			if err != nil {
				return err
			}

			fmt.Printf("Checkpoint: %s %q (saved %s)\n", chk.ID, chk.Label, chk.Timestamp.Format(time.RFC3339))

			if len(chk.Scrollback) > 0 {
				fmt.Println("\n--- Scrollback at checkpoint ---")
				os.Stdout.Write(chk.Scrollback)
				fmt.Println("\n--- End scrollback ---")
			}

			if chk.GitStash != "" && !noGit {
				fmt.Printf("\nRestore git stash (%s)? [y/N] ", chk.GitStash)
				var answer string
				fmt.Scanln(&answer)
				if strings.ToLower(strings.TrimSpace(answer)) == "y" {
					if err := checkpoint.Rollback(chk, true); err != nil {
						return err
					}
					successStyle.Println("✓ Git stash restored")
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "Session ID (required)")
	cmd.Flags().BoolVar(&noGit, "no-git", false, "Skip git stash restore")
	_ = cmd.MarkFlagRequired("session")

	return cmd
}

func replayCmd() *cobra.Command {
	var server string
	var speed float64
	var fromFile string
	var exportTo string

	cmd := &cobra.Command{
		Use:   "replay [session-id]",
		Short: "Replay the activity log of a session",
		Long: `Replay the activity log of a session.

You can either replay from a live relay server or from a saved log file.

Examples:
  # Replay from relay server
  pair-share replay swift-koala-42 --server ws://localhost:8080

  # Export log to file for later replay
  pair-share replay swift-koala-42 --server ws://localhost:8080 --export session.json

  # Replay from saved file (works offline, after session expires)
  pair-share replay --from-file session.json --speed 2`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var entries []actlog.LogEntry

			if fromFile != "" {
				// Load from local file
				data, err := os.ReadFile(fromFile)
				if err != nil {
					return fmt.Errorf("failed to read log file: %w", err)
				}
				if err := json.Unmarshal(data, &entries); err != nil {
					return fmt.Errorf("failed to parse log file: %w", err)
				}
				infoStyle.Printf("Loaded %d entries from %s\n", len(entries), fromFile)
			} else {
				// Fetch from relay server
				if len(args) == 0 {
					return fmt.Errorf("session-id is required when not using --from-file")
				}
				sessionID := args[0]
				httpURL := wsToHTTP(server)

				resp, err := http.Get(fmt.Sprintf("%s/session/%s/log", httpURL, sessionID))
				if err != nil {
					return fmt.Errorf("failed to contact relay: %w", err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != 200 {
					body, _ := io.ReadAll(resp.Body)
					return fmt.Errorf("relay error: %s", strings.TrimSpace(string(body)))
				}

				if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
					return fmt.Errorf("failed to decode log: %w", err)
				}
			}

			if len(entries) == 0 {
				fmt.Println("No activity log entries found.")
				return nil
			}

			// Export to file if requested
			if exportTo != "" {
				data, err := json.MarshalIndent(entries, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to serialize log: %w", err)
				}
				if err := os.WriteFile(exportTo, data, 0644); err != nil {
					return fmt.Errorf("failed to write log file: %w", err)
				}
				successStyle.Printf("✓ Exported %d entries to %s\n", len(entries), exportTo)
				return nil
			}

			fmt.Printf("Replaying %d log entries (speed: %.1fx)...\n\n", len(entries), speed)

			hostColor := color.New(color.Reset)
			guestColor := color.New(color.FgWhite)
			agentColor := color.New(color.FgYellow)

			var prevTime time.Time
			for i, entry := range entries {
				if entry.IsControl {
					continue
				}

				// Sleep proportional to real timing
				if i > 0 && !prevTime.IsZero() {
					delta := entry.Timestamp.Sub(prevTime)
					if delta > 0 && speed > 0 {
						sleepDuration := time.Duration(float64(delta) / speed)
						if sleepDuration > 2*time.Second {
							sleepDuration = 2 * time.Second
						}
						time.Sleep(sleepDuration)
					}
				}
				prevTime = entry.Timestamp

				data, err := base64.StdEncoding.DecodeString(entry.DataB64)
				if err != nil {
					continue
				}

				switch entry.Source {
				case "agent":
					agentColor.Print(string(data))
				case "guest":
					guestColor.Print(string(data))
				default:
					hostColor.Print(string(data))
				}
			}

			fmt.Println("\n\n[replay complete]")
			return nil
		},
	}

	cmd.Flags().StringVar(&server, "server", defaultServer, "Relay server URL")
	cmd.Flags().Float64Var(&speed, "speed", 1.0, "Playback speed multiplier (e.g., 2.0 for 2x)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Replay from a saved log file instead of relay")
	cmd.Flags().StringVar(&exportTo, "export", "", "Export the log to a file instead of replaying")

	return cmd
}

func summonCmd() *cobra.Command {
	var server string
	var sessionID string
	var access string
	var ttl string

	cmd := &cobra.Command{
		Use:   "summon <agent>",
		Short: "Grant an AI agent temporary access to the current session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := args[0]
			httpURL := wsToHTTP(server)

			reqURL := fmt.Sprintf("%s/session/%s/summon?access=%s&ttl=%s",
				httpURL, sessionID, url.QueryEscape(access), url.QueryEscape(ttl))

			resp, err := http.Post(reqURL, "application/json", nil)
			if err != nil {
				return fmt.Errorf("failed to contact relay: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("relay error: %s", strings.TrimSpace(string(body)))
			}

			var result struct {
				Token  string `json:"token"`
				Access string `json:"access"`
				TTL    string `json:"ttl"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			successStyle.Printf("✓ Agent access granted for %s\n", result.TTL)
			fmt.Printf("  Agent:  %s\n", agentName)
			fmt.Printf("  Token:  %s\n", result.Token)
			fmt.Printf("  Access: %s\n", result.Access)
			fmt.Println()
			sessionStyle.Printf("  Run in agent:\n")
			fmt.Printf("    pair-share join %s --server %s --token %s --role agent\n",
				sessionID, server, result.Token)

			return nil
		},
	}

	cmd.Flags().StringVar(&server, "server", defaultServer, "Relay server URL")
	cmd.Flags().StringVar(&sessionID, "session", "", "Session ID (required)")
	cmd.Flags().StringVar(&access, "access", "watch", "Access level: watch or control")
	cmd.Flags().StringVar(&ttl, "ttl", "120s", "How long to grant access")
	_ = cmd.MarkFlagRequired("session")

	return cmd
}

// wsToHTTP converts a WebSocket URL to its HTTP equivalent for REST calls.
func wsToHTTP(wsURL string) string {
	u, err := url.Parse(wsURL)
	if err != nil {
		return wsURL
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	return u.String()
}
