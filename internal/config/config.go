// Package config handles loading, validating, and atomically saving the
// hypr-ws-brightness TOML configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/adam0307a/hypr-ws-brightness/internal/brightness"
)

const (
	// DefaultDevice tells the brightness controller to auto-detect the backlight.
	DefaultDevice = "auto"
	// DefaultBrightnessValue is the fallback percentage for unconfigured workspaces.
	DefaultBrightnessValue = 50
	// DefaultDelayMs is the default delay (in ms) before brightening (low -> high).
	DefaultDelayMs = 0
	// MinDelayMs is the minimum allowed apply delay in milliseconds.
	MinDelayMs = 0
	// MaxDelayMs is the maximum allowed apply delay in milliseconds (30 seconds).
	MaxDelayMs = 30000
	// DefaultTransitionMs is the default smooth fade duration (in ms) when brightening (low -> high).
	DefaultTransitionMs = 800
	// DefaultDimTransitionMs is the default fast fade duration (in ms) when dimming (high -> low).
	DefaultDimTransitionMs = 100
	// MinTransitionMs is the minimum allowed smooth transition duration (0 = sharp/instant jump).
	MinTransitionMs = 0
	// MaxTransitionMs is the maximum allowed smooth transition duration (10 seconds).
	MaxTransitionMs = 10000
	// AppDirName is the subdirectory name under XDG_CONFIG_HOME / ~/.config.
	AppDirName = "hypr-ws-brightness"
	// FileName is the configuration file name.
	FileName = "config.toml"
)

// ClampDelay restricts a delay duration in milliseconds to [MinDelayMs, MaxDelayMs].
func ClampDelay(ms int) int {
	if ms < MinDelayMs {
		return MinDelayMs
	}
	if ms > MaxDelayMs {
		return MaxDelayMs
	}
	return ms
}

// ClampTransition restricts a smooth transition duration in milliseconds to [MinTransitionMs, MaxTransitionMs].
func ClampTransition(ms int) int {
	if ms < MinTransitionMs {
		return MinTransitionMs
	}
	if ms > MaxTransitionMs {
		return MaxTransitionMs
	}
	return ms
}

// Config holds user preferences and workspace-to-brightness mappings.
type Config struct {
	Device            string
	DefaultBrightness int
	DelayMs           int
	TransitionMs      int
	DimTransitionMs   int
	InstantWorkspaces map[int]bool
	Workspaces        map[int]int
}

type rawConfig struct {
	Device            *string        `toml:"device"`
	DefaultBrightness *int           `toml:"default_brightness"`
	DelayMs           *int           `toml:"delay_ms"`
	TransitionMs      *int           `toml:"transition_ms"`
	DimTransitionMs   *int           `toml:"dim_transition_ms"`
	InstantWorkspaces []int          `toml:"instant_workspaces"`
	Workspaces        map[string]int `toml:"workspaces"`
}

// LoadResult summarizes the outcome of loading a configuration file from disk.
type LoadResult struct {
	Config   *Config
	Exists   bool
	Corrupt  bool
	Warnings []string
}

// Default returns a new Config populated with safe default settings.
func Default() *Config {
	return &Config{
		Device:            DefaultDevice,
		DefaultBrightness: DefaultBrightnessValue,
		DelayMs:           DefaultDelayMs,
		TransitionMs:      DefaultTransitionMs,
		DimTransitionMs:   DefaultDimTransitionMs,
		InstantWorkspaces: make(map[int]bool),
		Workspaces:        make(map[int]int),
	}
}

// SampleTOML returns the commented starter TOML written by `hypr-ws-brightness init`.
func SampleTOML() string {
	return `# hypr-ws-brightness configuration
# Backlight device name (run 'brightnessctl --list' to inspect) or "auto"
device = "auto"

# Fallback brightness percentage (0-100) for workspaces without an explicit rule
default_brightness = 50

# Delay in milliseconds (0-30000) before brightening when switching from a darker workspace
# to a brighter one. Dimming (high -> low) always begins immediately (0ms delay).
delay_ms = 500

# Slow, gentle fade duration in ms (0-10000) when brightening (low -> high)
transition_ms = 800

# Fast/instant dim duration in ms (0-10000) when dimming (high -> low; 0 = instant drop)
dim_transition_ms = 100

# Workspace IDs that always skip delay_ms (0ms delay even when brightening)
instant_workspaces = []

[workspaces]
1 = 70
2 = 40
3 = 30
`
}

// DefaultPath returns the standard configuration path
// ($XDG_CONFIG_HOME/hypr-ws-brightness/config.toml or ~/.config/hypr-ws-brightness/config.toml).
func DefaultPath() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, AppDirName, FileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user home directory: %w", err)
	}
	return filepath.Join(home, ".config", AppDirName, FileName), nil
}

// Parse decodes TOML text into a Config. Out-of-range values are clamped with warnings.
// If the TOML syntax or field types are invalid, Parse returns Default() alongside the parse error.
func Parse(data string) (*Config, []string, error) {
	var raw rawConfig
	meta, err := toml.Decode(data, &raw)
	if err != nil {
		return Default(), nil, err
	}

	cfg := Default()
	var warnings []string

	for _, key := range meta.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("unknown configuration key %q ignored", key.String()))
	}

	if raw.Device != nil {
		dev := strings.TrimSpace(*raw.Device)
		if dev == "" {
			cfg.Device = DefaultDevice
		} else {
			cfg.Device = dev
		}
	}

	if raw.DefaultBrightness != nil {
		val := *raw.DefaultBrightness
		clamped := brightness.Clamp(val)
		if clamped != val {
			warnings = append(warnings, fmt.Sprintf(
				"default_brightness (%d) is outside 0-100; clamped to %d",
				val, clamped,
			))
		}
		cfg.DefaultBrightness = clamped
	}

	if raw.DelayMs != nil {
		val := *raw.DelayMs
		clamped := ClampDelay(val)
		if clamped != val {
			warnings = append(warnings, fmt.Sprintf(
				"delay_ms (%d) is outside %d-%d; clamped to %d",
				val, MinDelayMs, MaxDelayMs, clamped,
			))
		}
		cfg.DelayMs = clamped
	}

	if raw.TransitionMs != nil {
		val := *raw.TransitionMs
		clamped := ClampTransition(val)
		if clamped != val {
			warnings = append(warnings, fmt.Sprintf(
				"transition_ms (%d) is outside %d-%d; clamped to %d",
				val, MinTransitionMs, MaxTransitionMs, clamped,
			))
		}
		cfg.TransitionMs = clamped
	}

	if raw.DimTransitionMs != nil {
		val := *raw.DimTransitionMs
		clamped := ClampTransition(val)
		if clamped != val {
			warnings = append(warnings, fmt.Sprintf(
				"dim_transition_ms (%d) is outside %d-%d; clamped to %d",
				val, MinTransitionMs, MaxTransitionMs, clamped,
			))
		}
		cfg.DimTransitionMs = clamped
	}

	for _, wsID := range raw.InstantWorkspaces {
		if wsID <= 0 {
			warnings = append(warnings, fmt.Sprintf(
				"invalid workspace ID %d in instant_workspaces ignored (must be positive)",
				wsID,
			))
			continue
		}
		cfg.InstantWorkspaces[wsID] = true
	}

	if len(raw.Workspaces) > 0 {
		keys := make([]string, 0, len(raw.Workspaces))
		for k := range raw.Workspaces {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			val := raw.Workspaces[k]
			id, convErr := strconv.Atoi(strings.TrimSpace(k))
			if convErr != nil || id <= 0 {
				warnings = append(warnings, fmt.Sprintf(
					"invalid workspace ID %q in [workspaces] ignored (must be a positive integer)",
					k,
				))
				continue
			}

			clamped := brightness.Clamp(val)
			if clamped != val {
				warnings = append(warnings, fmt.Sprintf(
					"workspace %d brightness (%d) is outside 0-100; clamped to %d",
					id, val, clamped,
				))
			}
			cfg.Workspaces[id] = clamped
		}
	}

	return cfg, warnings, nil
}

// Load reads and parses a TOML configuration file from path.
// It never returns a nil Config: if the file is missing or corrupt, Load falls
// back to Default() and populates Exists, Corrupt, and Warnings accordingly.
func Load(path string) LoadResult {
	rawBytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoadResult{
				Config: Default(),
				Exists: false,
			}
		}
		return LoadResult{
			Config:  Default(),
			Exists:  true,
			Corrupt: true,
			Warnings: []string{
				fmt.Sprintf("failed to read config file %q (%v); falling back to defaults", path, err),
			},
		}
	}

	cfg, warnings, parseErr := Parse(string(rawBytes))
	if parseErr != nil {
		return LoadResult{
			Config:  Default(),
			Exists:  true,
			Corrupt: true,
			Warnings: []string{
				fmt.Sprintf("corrupt config file %q (%v); falling back to defaults", path, parseErr),
			},
		}
	}

	return LoadResult{
		Config:   cfg,
		Exists:   true,
		Corrupt:  false,
		Warnings: warnings,
	}
}

// BrightnessFor returns the effective brightness percentage (0-100) for workspaceID
// and a boolean indicating whether an explicit workspace rule existed.
func (c *Config) BrightnessFor(workspaceID int) (int, bool) {
	if c == nil {
		return DefaultBrightnessValue, false
	}
	if val, ok := c.Workspaces[workspaceID]; ok {
		return brightness.Clamp(val), true
	}
	return brightness.Clamp(c.DefaultBrightness), false
}

// IsInstantWorkspace reports whether workspaceID is configured to always bypass delay_ms.
func (c *Config) IsInstantWorkspace(workspaceID int) bool {
	if c == nil || c.InstantWorkspaces == nil {
		return false
	}
	return c.InstantWorkspaces[workspaceID]
}

// ToggleInstantWorkspace flips the 0ms-delay override for workspaceID and returns the new state.
func (c *Config) ToggleInstantWorkspace(workspaceID int) bool {
	if c.InstantWorkspaces == nil {
		c.InstantWorkspaces = make(map[int]bool)
	}
	if c.InstantWorkspaces[workspaceID] {
		delete(c.InstantWorkspaces, workspaceID)
		return false
	}
	c.InstantWorkspaces[workspaceID] = true
	return true
}

// SortedInstantWorkspaceIDs returns all instant-override workspace IDs in ascending order.
func (c *Config) SortedInstantWorkspaceIDs() []int {
	ids := make([]int, 0, len(c.InstantWorkspaces))
	for id, enabled := range c.InstantWorkspaces {
		if enabled && id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids
}

// SetWorkspace assigns a clamped brightness percentage to workspaceID.
func (c *Config) SetWorkspace(workspaceID, percent int) {
	if c.Workspaces == nil {
		c.Workspaces = make(map[int]int)
	}
	c.Workspaces[workspaceID] = brightness.Clamp(percent)
}

// DeleteWorkspace removes any custom brightness rule and instant flag for workspaceID.
func (c *Config) DeleteWorkspace(workspaceID int) {
	delete(c.Workspaces, workspaceID)
	delete(c.InstantWorkspaces, workspaceID)
}

// SortedWorkspaceIDs returns all configured workspace IDs in ascending order.
func (c *Config) SortedWorkspaceIDs() []int {
	ids := make([]int, 0, len(c.Workspaces))
	for id := range c.Workspaces {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// MarshalTOML renders Config into clean, human-readable TOML with sorted workspace IDs.
func (c *Config) MarshalTOML() string {
	dev := strings.TrimSpace(c.Device)
	if dev == "" {
		dev = DefaultDevice
	}
	defBrightness := brightness.Clamp(c.DefaultBrightness)
	delayMs := ClampDelay(c.DelayMs)
	transitionMs := ClampTransition(c.TransitionMs)
	dimTransitionMs := ClampTransition(c.DimTransitionMs)

	instantIDs := c.SortedInstantWorkspaceIDs()
	instantStrs := make([]string, 0, len(instantIDs))
	for _, id := range instantIDs {
		instantStrs = append(instantStrs, strconv.Itoa(id))
	}

	var b strings.Builder
	b.WriteString("# hypr-ws-brightness configuration\n")
	fmt.Fprintf(&b, "device = %s\n", strconv.Quote(dev))
	fmt.Fprintf(&b, "default_brightness = %d\n", defBrightness)
	fmt.Fprintf(&b, "delay_ms = %d\n", delayMs)
	fmt.Fprintf(&b, "transition_ms = %d\n", transitionMs)
	fmt.Fprintf(&b, "dim_transition_ms = %d\n", dimTransitionMs)
	fmt.Fprintf(&b, "instant_workspaces = [%s]\n\n", strings.Join(instantStrs, ", "))
	b.WriteString("[workspaces]\n")

	for _, id := range c.SortedWorkspaceIDs() {
		fmt.Fprintf(&b, "%d = %d\n", id, brightness.Clamp(c.Workspaces[id]))
	}
	return b.String()
}

// Save atomically writes Config to path, creating parent directories if needed.
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create config directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config.toml.tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary config file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	content := c.MarshalTOML()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write temporary config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to sync temporary config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary config file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("failed to set permissions on config file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to replace config file %q: %w", path, err)
	}

	cleanup = false
	return nil
}
