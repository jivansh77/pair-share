package pty

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/jivansh77/pair-share/internal/session"
)

type PTY struct {
	File *os.File
	Cmd  *exec.Cmd
}

// Start forks the user's shell in a new PTY and returns a handle.
// The caller is responsible for reading/writing the returned File.
func Start() (*PTY, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	cmd := exec.Command(shell)
	cmd.Env = os.Environ()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	// Set initial size from the current terminal
	if cols, rows, err := GetTerminalSize(); err == nil {
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows})
	}

	return &PTY{File: ptmx, Cmd: cmd}, nil
}

func (p *PTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.File, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *PTY) Close() {
	p.File.Close()
	if p.Cmd.Process != nil {
		_ = p.Cmd.Process.Kill()
	}
	_ = p.Cmd.Wait()
}

// GetTerminalSize returns the current terminal dimensions.
func GetTerminalSize() (cols, rows uint16, err error) {
	fd := int(os.Stdin.Fd())
	w, h, err := term.GetSize(fd)
	if err != nil {
		return 0, 0, err
	}
	return uint16(w), uint16(h), nil
}

// WatchResize listens for SIGWINCH and calls the provided function
// with the new terminal size. It blocks until the stop channel is closed.
func WatchResize(onResize func(session.TermSize), stop <-chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-sigCh:
			cols, rows, err := GetTerminalSize()
			if err == nil {
				onResize(session.TermSize{Cols: cols, Rows: rows})
			}
		case <-stop:
			return
		}
	}
}
