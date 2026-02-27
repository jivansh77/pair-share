package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Checkpoint struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Timestamp  time.Time `json:"timestamp"`
	GitStash   string    `json:"git_stash,omitempty"`
	Scrollback []byte    `json:"scrollback"`
	SessionID  string    `json:"session_id"`
}

func GenerateID() (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "chk_" + hex.EncodeToString(b), nil
}

// Save creates a checkpoint with the given scrollback data and optional git stash.
func Save(sessionID, label string, scrollback []byte) (*Checkpoint, error) {
	id, err := GenerateID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate checkpoint ID: %w", err)
	}

	chk := &Checkpoint{
		ID:         id,
		Label:      label,
		Timestamp:  time.Now(),
		Scrollback: scrollback,
		SessionID:  sessionID,
	}

	// Try git stash if we're in a git repo
	if isGitRepo() {
		stashID, err := gitStash(label)
		if err == nil && stashID != "" {
			chk.GitStash = stashID
		}
	}

	if err := chk.writeToDisk(); err != nil {
		return nil, fmt.Errorf("failed to save checkpoint: %w", err)
	}

	return chk, nil
}

// Load reads a checkpoint from disk by label for a given session.
func Load(sessionID, label string) (*Checkpoint, error) {
	dir := checkpointDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no checkpoints found for session %s", sessionID)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var chk Checkpoint
		if err := json.Unmarshal(data, &chk); err != nil {
			continue
		}
		if chk.Label == label {
			return &chk, nil
		}
	}
	return nil, fmt.Errorf("checkpoint %q not found for session %s", label, sessionID)
}

// Rollback restores a checkpoint: prints scrollback and pops git stash.
func Rollback(chk *Checkpoint, confirm bool) error {
	if chk.GitStash != "" && confirm {
		cmd := exec.Command("git", "stash", "pop")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git stash pop failed: %w", err)
		}
	}
	return nil
}

func (c *Checkpoint) writeToDisk() error {
	dir := checkpointDir(c.SessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(dir, c.ID+".json")
	return os.WriteFile(path, data, 0o644)
}

func checkpointDir(sessionID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pair-share", "checkpoints", sessionID)
}

func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func gitStash(label string) (string, error) {
	msg := fmt.Sprintf("pair-share: %s", label)
	cmd := exec.Command("git", "stash", "push", "-m", msg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}

	output := strings.TrimSpace(string(out))
	if strings.Contains(output, "No local changes") {
		return "", nil
	}
	// Extract stash reference from output like "Saved working directory and index state..."
	return "stash@{0}", nil
}
