// Package hyprland provides helpers for Hyprland environment detection,
// IPC socket2 event parsing, and hyprctl JSON workspace queries.
package hyprland

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrNotInHyprland indicates HYPRLAND_INSTANCE_SIGNATURE is missing from the environment.
var ErrNotInHyprland = errors.New(
	"HYPRLAND_INSTANCE_SIGNATURE is not set; please run inside an active Hyprland session",
)

// Workspace represents a workspace object returned by `hyprctl workspaces -j`
// or `hyprctl activeworkspace -j`.
type Workspace struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Monitor         string `json:"monitor"`
	Windows         int    `json:"windows"`
	LastWindowTitle string `json:"lastwindowtitle"`
}

// WorkspaceEvent represents a parsed workspace transition from Hyprland's socket2 stream.
type WorkspaceEvent struct {
	Type  string // "workspacev2" or "workspace"
	ID    int    // Resolved numeric workspace ID when HasID is true
	Name  string // Workspace name reported by Hyprland
	HasID bool   // True when a numeric ID was extracted directly from the event payload
}

// Runner abstracts command execution for testing hyprctl queries.
type Runner interface {
	LookPath(file string) (string, error)
	Run(name string, args ...string) ([]byte, error)
}

type osRunner struct{}

func (osRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (osRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// InstanceSignature returns HYPRLAND_INSTANCE_SIGNATURE or ErrNotInHyprland if unset.
func InstanceSignature() (string, error) {
	sig := strings.TrimSpace(os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"))
	if sig == "" {
		return "", ErrNotInHyprland
	}
	return sig, nil
}

// InSession reports whether the current process appears to run inside Hyprland.
func InSession() bool {
	_, err := InstanceSignature()
	return err == nil
}

// Socket2Path locates Hyprland's event socket (.socket2.sock) for the active session.
func Socket2Path() (string, error) {
	sig, err := InstanceSignature()
	if err != nil {
		return "", err
	}
	return ResolveSocket2Path(os.Getenv("XDG_RUNTIME_DIR"), sig, os.Stat)
}

// ResolveSocket2Path resolves .socket2.sock using XDG_RUNTIME_DIR first and /tmp/hypr as fallback.
func ResolveSocket2Path(
	xdgRuntimeDir string,
	signature string,
	statFn func(string) (os.FileInfo, error),
) (string, error) {
	sig := strings.TrimSpace(signature)
	if sig == "" {
		return "", ErrNotInHyprland
	}
	if statFn == nil {
		statFn = os.Stat
	}

	var candidates []string
	if xdg := strings.TrimSpace(xdgRuntimeDir); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "hypr", sig, ".socket2.sock"))
	}
	candidates = append(candidates, filepath.Join("/tmp", "hypr", sig, ".socket2.sock"))

	for _, p := range candidates {
		if _, err := statFn(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf(
		"Hyprland IPC socket2 not found (checked: %s)",
		strings.Join(candidates, ", "),
	)
}

// ParseWorkspaceEvent parses a single line from Hyprland's socket2 stream.
// Both `workspacev2>>ID,NAME` and `workspace>>NAME` formats are recognized.
// Non-workspace events return (WorkspaceEvent{}, false, nil).
func ParseWorkspaceEvent(line string) (WorkspaceEvent, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return WorkspaceEvent{}, false, nil
	}

	evType, payload, ok := strings.Cut(trimmed, ">>")
	if !ok {
		return WorkspaceEvent{}, false, nil
	}

	switch evType {
	case "workspacev2":
		idPart, namePart, hasComma := strings.Cut(payload, ",")
		idStr := strings.TrimSpace(idPart)
		name := strings.TrimSpace(namePart)
		if !hasComma {
			name = idStr
		}
		if idStr == "" {
			return WorkspaceEvent{}, false, fmt.Errorf("empty workspace ID in event %q", trimmed)
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return WorkspaceEvent{}, false, fmt.Errorf("invalid workspace ID %q in event %q: %w", idStr, trimmed, err)
		}
		if strings.HasPrefix(name, "special:") || id <= 0 {
			return WorkspaceEvent{}, false, nil
		}
		return WorkspaceEvent{
			Type:  "workspacev2",
			ID:    id,
			Name:  name,
			HasID: true,
		}, true, nil

	case "workspace":
		name := strings.TrimSpace(payload)
		if name == "" {
			return WorkspaceEvent{}, false, fmt.Errorf("empty workspace name in event %q", trimmed)
		}
		if strings.HasPrefix(name, "special:") {
			return WorkspaceEvent{}, false, nil
		}
		if id, err := strconv.Atoi(name); err == nil {
			if id <= 0 {
				return WorkspaceEvent{}, false, nil
			}
			return WorkspaceEvent{
				Type:  "workspace",
				ID:    id,
				Name:  name,
				HasID: true,
			}, true, nil
		}
		return WorkspaceEvent{
			Type:  "workspace",
			Name:  name,
			HasID: false,
		}, true, nil

	default:
		return WorkspaceEvent{}, false, nil
	}
}

// Client queries Hyprland state via hyprctl.
type Client struct {
	runner Runner
}

// NewClient creates a Hyprland IPC query client.
func NewClient() *Client {
	return &Client{runner: osRunner{}}
}

// NewClientWithRunner creates a Client with an injected Runner for unit tests.
func NewClientWithRunner(r Runner) *Client {
	if r == nil {
		r = osRunner{}
	}
	return &Client{runner: r}
}

// Workspaces returns all currently open regular workspaces (ID > 0), sorted by ID.
func (c *Client) Workspaces() ([]Workspace, error) {
	if _, err := c.runner.LookPath("hyprctl"); err != nil {
		return nil, fmt.Errorf("hyprctl executable not found in PATH: %w", err)
	}
	out, err := c.runner.Run("hyprctl", "workspaces", "-j")
	if err != nil {
		return nil, fmt.Errorf("hyprctl workspaces -j failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	var raw []Workspace
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode 'hyprctl workspaces -j' JSON: %w", err)
	}

	filtered := make([]Workspace, 0, len(raw))
	for _, ws := range raw {
		if ws.ID > 0 && !strings.HasPrefix(ws.Name, "special:") {
			filtered = append(filtered, ws)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})
	return filtered, nil
}

// ActiveWorkspace queries `hyprctl activeworkspace -j` and returns the focused workspace.
func (c *Client) ActiveWorkspace() (Workspace, error) {
	if _, err := c.runner.LookPath("hyprctl"); err != nil {
		return Workspace{}, fmt.Errorf("hyprctl executable not found in PATH: %w", err)
	}
	out, err := c.runner.Run("hyprctl", "activeworkspace", "-j")
	if err != nil {
		return Workspace{}, fmt.Errorf("hyprctl activeworkspace -j failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	var ws Workspace
	if err := json.Unmarshal(out, &ws); err != nil {
		return Workspace{}, fmt.Errorf("failed to decode 'hyprctl activeworkspace -j' JSON: %w", err)
	}
	return ws, nil
}
