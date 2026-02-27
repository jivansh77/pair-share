package main

import (
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/jivansh77/pair-share/internal/client"
	"github.com/jivansh77/pair-share/internal/relay"
	"github.com/jivansh77/pair-share/internal/session"
)

var defaultServer = "ws://localhost:8080"

func main() {
	rootCmd := &cobra.Command{
		Use:   "pair-share",
		Short: "Real-time terminal sharing for pair debugging",
		Long:  "A lightweight CLI tool for secure, real-time terminal sharing, purpose-built for pair debugging and AI agent collaboration.",
	}

	rootCmd.AddCommand(startCmd())
	rootCmd.AddCommand(joinCmd())
	rootCmd.AddCommand(serveCmd())

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
			})
		},
	}

	cmd.Flags().StringVar(&server, "server", defaultServer, "Relay server URL")
	cmd.Flags().BoolVar(&watch, "watch", false, "Join in watch-only mode")
	cmd.Flags().StringVar(&password, "password", "", "Session password")

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
