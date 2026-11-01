package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseValidTOML(t *testing.T) {
	input := `
device = "amdgpu_bl2"
default_brightness = 55

[workspaces]
1 = 70
2 = 40
3 = 30
`
	cfg, warnings, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %v", warnings)
	}
	if cfg.Device != "amdgpu_bl2" {
		t.Errorf("Device = %q, want amdgpu_bl2", cfg.Device)
	}
	if cfg.DefaultBrightness != 55 {
		t.Errorf("DefaultBrightness = %d, want 55", cfg.DefaultBrightness)
	}

	if v, ok := cfg.BrightnessFor(1); !ok || v != 70 {
		t.Errorf("BrightnessFor(1) = (%d, %v), want (70, true)", v, ok)
	}
	if v, ok := cfg.BrightnessFor(9); ok || v != 55 {
		t.Errorf("BrightnessFor(9) = (%d, %v), want (55, false)", v, ok)
	}
}

func TestParseOmittedVsZeroDefaultBrightness(t *testing.T) {
	omitted, _, err := Parse(`[workspaces]` + "\n" + `1 = 80`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if omitted.DefaultBrightness != DefaultBrightnessValue {
		t.Errorf("omitted DefaultBrightness = %d, want %d", omitted.DefaultBrightness, DefaultBrightnessValue)
	}

	explicitZero, _, err := Parse(`default_brightness = 0`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if explicitZero.DefaultBrightness != 0 {
		t.Errorf("explicit zero DefaultBrightness = %d, want 0", explicitZero.DefaultBrightness)
	}
}

func TestParseClampingAndInvalidKeys(t *testing.T) {
	input := `
device = ""
default_brightness = 140

[workspaces]
1 = -20
2 = 180
abc = 50
0 = 40
`
	cfg, warnings, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse unexpected error: %v", err)
	}
	if cfg.Device != "auto" {
		t.Errorf("empty device should normalize to 'auto', got %q", cfg.Device)
	}
	if cfg.DefaultBrightness != 100 {
		t.Errorf("DefaultBrightness = %d, want 100", cfg.DefaultBrightness)
	}
	if cfg.Workspaces[1] != 0 {
		t.Errorf("Workspaces[1] = %d, want 0", cfg.Workspaces[1])
	}
	if cfg.Workspaces[2] != 100 {
		t.Errorf("Workspaces[2] = %d, want 100", cfg.Workspaces[2])
	}
	if _, exists := cfg.Workspaces[0]; exists {
		t.Errorf("workspace 0 should be rejected")
	}
	if len(warnings) != 5 {
		t.Errorf("expected 5 warnings (default clamp, ws1 clamp, ws2 clamp, invalid '0', invalid 'abc'), got %d: %v", len(warnings), warnings)
	}
}

func TestLoadCorruptAndMissingFallback(t *testing.T) {
	tmpDir := t.TempDir()

	// Missing file -> quiet fallback to defaults.
	missingPath := filepath.Join(tmpDir, "missing.toml")
	res := Load(missingPath)
	if res.Exists || res.Corrupt {
		t.Errorf("missing file: Exists=%v Corrupt=%v, want false/false", res.Exists, res.Corrupt)
	}
	if res.Config.DefaultBrightness != DefaultBrightnessValue {
		t.Errorf("missing fallback DefaultBrightness = %d, want %d", res.Config.DefaultBrightness, DefaultBrightnessValue)
	}

	// Corrupt TOML -> Corrupt=true, warning emitted, safe fallback to defaults.
	corruptPath := filepath.Join(tmpDir, "corrupt.toml")
	if err := os.WriteFile(corruptPath, []byte("default_brightness = [broken\n1 == ???"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = Load(corruptPath)
	if !res.Exists || !res.Corrupt {
		t.Errorf("corrupt file: Exists=%v Corrupt=%v, want true/true", res.Exists, res.Corrupt)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "falling back to defaults") {
		t.Errorf("expected fallback warning, got %v", res.Warnings)
	}
	if res.Config.Device != DefaultDevice || res.Config.DefaultBrightness != DefaultBrightnessValue {
		t.Errorf("corrupt fallback config unexpected: %+v", res.Config)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "nested", "config.toml")

	cfg := Default()
	cfg.Device = "amdgpu_bl2"
	cfg.DefaultBrightness = 45
	cfg.DelayMs = 750
	cfg.SetWorkspace(10, 90)
	cfg.SetWorkspace(2, 35)
	cfg.SetWorkspace(1, 75)

	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	// Verify numeric ordering (1, 2, 10 rather than 1, 10, 2).
	idx1 := strings.Index(string(raw), "1 = 75")
	idx2 := strings.Index(string(raw), "2 = 35")
	idx10 := strings.Index(string(raw), "10 = 90")
	if !(idx1 >= 0 && idx1 < idx2 && idx2 < idx10) {
		t.Errorf("expected sorted workspace keys in output:\n%s", string(raw))
	}

	loaded := Load(cfgPath)
	if !loaded.Exists || loaded.Corrupt || len(loaded.Warnings) > 0 {
		t.Fatalf("unexpected Load result: %+v", loaded)
	}
	if loaded.Config.Device != "amdgpu_bl2" || loaded.Config.DefaultBrightness != 45 || loaded.Config.DelayMs != 750 {
		t.Errorf("round-trip mismatch: %+v", loaded.Config)
	}
	if v, ok := loaded.Config.BrightnessFor(10); !ok || v != 90 {
		t.Errorf("BrightnessFor(10) = (%d, %v), want (90, true)", v, ok)
	}
}

func TestDelayMsParsingAndClamping(t *testing.T) {
	cfg, warnings, err := Parse("delay_ms = -200\ntransition_ms = -50\ninstant_workspaces = [5, 1, -2]\n")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.DelayMs != 0 || cfg.TransitionMs != 0 {
		t.Errorf("negative delay/transition: delay=%d transition=%d", cfg.DelayMs, cfg.TransitionMs)
	}
	if !cfg.IsInstantWorkspace(1) || !cfg.IsInstantWorkspace(5) || cfg.IsInstantWorkspace(-2) {
		t.Errorf("unexpected instant_workspaces: %+v", cfg.InstantWorkspaces)
	}
	if len(warnings) != 3 {
		t.Errorf("expected 3 warnings, got %v", warnings)
	}

	cfg, warnings, err = Parse("delay_ms = 99999\ntransition_ms = 50000\n")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.DelayMs != MaxDelayMs || cfg.TransitionMs != MaxTransitionMs || len(warnings) != 2 {
		t.Errorf("excessive delay/transition: delay=%d transition=%d warnings=%v", cfg.DelayMs, cfg.TransitionMs, warnings)
	}
}
