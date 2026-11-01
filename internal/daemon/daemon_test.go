package daemon

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adam0307a/hypr-ws-brightness/internal/brightness"
	"github.com/adam0307a/hypr-ws-brightness/internal/hyprland"
)

type fakeSetter struct {
	mu       sync.Mutex
	device   string
	maxRaw   int
	curRaw   int
	curPct   int
	applied  []int
	rawSteps []int
	notify   chan int
}

func newFakeSetter(initialPct, maxRaw int) *fakeSetter {
	if maxRaw <= 0 {
		maxRaw = 10000
	}
	return &fakeSetter{
		device: "amdgpu_bl2",
		maxRaw: maxRaw,
		curPct: initialPct,
		curRaw: brightness.PercentToRaw(initialPct, maxRaw),
		notify: make(chan int, 16),
	}
}

func (f *fakeSetter) Device() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.device == "" {
		return "amdgpu_bl2"
	}
	return f.device
}

func (f *fakeSetter) GetInfo() (brightness.DeviceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	maxVal := f.maxRaw
	if maxVal <= 0 {
		maxVal = 10000
	}
	return brightness.DeviceInfo{
		Name:       "amdgpu_bl2",
		Class:      "backlight",
		Current:    f.curRaw,
		Percentage: f.curPct,
		Max:        maxVal,
	}, nil
}

func (f *fakeSetter) Set(percent int) (int, error) {
	c := brightness.Clamp(percent)
	f.mu.Lock()
	f.applied = append(f.applied, c)
	f.curPct = c
	if f.maxRaw > 0 {
		f.curRaw = brightness.PercentToRaw(c, f.maxRaw)
	}
	ch := f.notify
	f.mu.Unlock()
	if ch != nil {
		select {
		case ch <- c:
		default:
		}
	}
	return c, nil
}

func (f *fakeSetter) SetRaw(raw, max int) error {
	f.mu.Lock()
	f.rawSteps = append(f.rawSteps, raw)
	f.curRaw = raw
	f.curPct = brightness.RawToPercent(raw, max)
	f.mu.Unlock()
	return nil
}

func (f *fakeSetter) ResolveDevice(configuredDevice string, _ func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device = configuredDevice
	return nil
}

func (f *fakeSetter) snapshot() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.applied))
	copy(out, f.applied)
	return out
}

func (f *fakeSetter) rawSnapshot() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.rawSteps))
	copy(out, f.rawSteps)
	return out
}

type fakeQuerier struct {
	activeID int
}

func (f *fakeQuerier) ActiveWorkspace() (hyprland.Workspace, error) {
	return hyprland.Workspace{ID: f.activeID, Name: "named"}, nil
}

func TestDebouncer(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	deb := NewDebouncer(100 * time.Millisecond)
	deb.now = func() time.Time { return now }

	if !deb.ShouldProcess(1) {
		t.Fatal("first event for workspace 1 should be processed")
	}

	// Duplicate event 5ms later (e.g. workspacev2 right after workspace) -> ignored.
	now = now.Add(5 * time.Millisecond)
	if deb.ShouldProcess(1) {
		t.Fatal("duplicate event within 100ms should be debounced")
	}

	// Different workspace 20ms later -> processed immediately.
	now = now.Add(20 * time.Millisecond)
	if !deb.ShouldProcess(2) {
		t.Fatal("different workspace ID should not be debounced")
	}

	// Switching right back to workspace 1 after 20ms -> processed immediately.
	now = now.Add(20 * time.Millisecond)
	if !deb.ShouldProcess(1) {
		t.Fatal("switching back to workspace 1 from workspace 2 should not be debounced")
	}

	// Same workspace 1 after 100ms window expires -> processed.
	now = now.Add(105 * time.Millisecond)
	if !deb.ShouldProcess(1) {
		t.Fatal("same workspace after 100ms window should be processed")
	}
}

func TestDaemonProcessStreamDeduplicationAndFallback(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.toml")
	cfgContent := `
device = "auto"
default_brightness = 50
delay_ms = 0
transition_ms = 0
dim_transition_ms = 0

[workspaces]
1 = 75
2 = 35
3 = 25
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	setter := newFakeSetter(50, 10000)
	querier := &fakeQuerier{activeID: 3}
	deb := NewDebouncer(100 * time.Millisecond)

	d := NewWithDeps(
		Options{ConfigPath: cfgPath, DebounceWindow: 100 * time.Millisecond},
		setter,
		querier,
		deb,
	)

	stream := strings.Join([]string{
		"activewindow>>kitty,zsh",
		"workspace>>1",
		"workspacev2>>1,1", // Duplicate within <100ms -> debounced
		"workspacev2>>2,2",
		"workspace>>2",     // Duplicate within <100ms -> debounced
		"workspace>>9",     // Unconfigured workspace -> default_brightness (50)
		"workspacev2>>9,9", // Duplicate -> debounced
		"workspace>>web",   // Named workspace -> resolved via querier (ID=3 -> 25%)
		"workspacev2>>3,web",
	}, "\n") + "\n"

	if err := d.ProcessStream(context.Background(), strings.NewReader(stream)); err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}

	want := []int{75, 35, 50, 25}
	if got := setter.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("applied brightness sequence = %v, want %v", got, want)
	}
}

func TestDaemonAsymmetricDelayAndInstantWorkspaces(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.toml")
	cfgContent := `
device = "auto"
default_brightness = 50
delay_ms = 220
transition_ms = 0
dim_transition_ms = 0
instant_workspaces = [5]

[workspaces]
1 = 80
2 = 10
3 = 45
5 = 95
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Start at 80% brightness.
	setter := newFakeSetter(80, 10000)
	d := NewWithDeps(
		Options{ConfigPath: cfgPath, DebounceWindow: 40 * time.Millisecond},
		setter,
		nil,
		nil,
	)

	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- d.ProcessStream(ctx, pr)
	}()

	// 1. High -> Low (80% -> 10% on WS 2): MUST apply immediately (0ms delay), not wait 220ms!
	_, _ = io.WriteString(pw, "workspace>>2\nworkspacev2>>2,2\n")
	select {
	case val := <-setter.notify:
		if val != 10 {
			t.Fatalf("expected immediate dim to 10%%, got %d%%", val)
		}
	case <-time.After(80 * time.Millisecond):
		t.Fatal("dimming (high -> low) should have 0ms delay, but timed out")
	}

	// 2. Low -> High (10% -> 80% on WS 1), then pass through to WS 3 (45%) before 220ms expires:
	// WS 1 (80%) must be cancelled without flashing, and WS 3 (45%) fires after 220ms.
	_, _ = io.WriteString(pw, "workspace>>1\nworkspacev2>>1,1\n")
	time.Sleep(50 * time.Millisecond)
	if got := setter.snapshot(); !reflect.DeepEqual(got, []int{10}) {
		t.Fatalf("brightening to WS 1 should still be delayed, got %v", got)
	}
	_, _ = io.WriteString(pw, "workspace>>3\nworkspacev2>>3,3\n")
	select {
	case val := <-setter.notify:
		if val != 45 {
			t.Fatalf("expected delayed brighten to 45%% (WS 3), got %d%%", val)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for delayed brighten to WS 3")
	}

	// 3. Low -> High (45% -> 95% on WS 5), where WS 5 is in instant_workspaces:
	// MUST bypass the 220ms brighten delay and apply immediately!
	_, _ = io.WriteString(pw, "workspace>>5\nworkspacev2>>5,5\n")
	select {
	case val := <-setter.notify:
		if val != 95 {
			t.Fatalf("expected instant brighten to 95%% (WS 5), got %d%%", val)
		}
	case <-time.After(80 * time.Millisecond):
		t.Fatal("instant_workspaces entry (WS 5) should bypass delay_ms")
	}

	_ = pw.Close()
	if err := <-doneCh; err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}

	want := []int{10, 45, 95}
	if got := setter.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("applied sequence = %v, want %v", got, want)
	}
}

func TestDaemonSmoothTransitionGradualSteps(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.toml")
	cfgContent := `
device = "auto"
default_brightness = 50
delay_ms = 0
transition_ms = 90
dim_transition_ms = 0

[workspaces]
1 = 90
2 = 10
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Start at 10% (raw 1000 out of 10000), fade smoothly to 90% (raw 9000).
	setter := newFakeSetter(10, 10000)
	d := NewWithDeps(
		Options{
			ConfigPath:     cfgPath,
			DebounceWindow: 30 * time.Millisecond,
			FadeInterval:   15 * time.Millisecond,
		},
		setter,
		nil,
		nil,
	)

	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- d.ProcessStream(ctx, pr)
	}()

	_, _ = io.WriteString(pw, "workspace>>1\n")

	select {
	case finalPct := <-setter.notify:
		if finalPct != 90 {
			t.Fatalf("finalPct = %d, want 90", finalPct)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for smooth transition completion")
	}

	rawSteps := setter.rawSnapshot()
	if len(rawSteps) < 2 {
		t.Fatalf("expected multiple intermediate smooth raw steps on brighten, got %v", rawSteps)
	}
	prev := 1000
	for _, step := range rawSteps {
		if step < prev || step > 9000 {
			t.Errorf("non-monotonic or out-of-bounds raw step %d (prev=%d, steps=%v)", step, prev, rawSteps)
		}
		prev = step
	}

	// Now dim back down 90% -> 10% (WS 2) with dim_transition_ms = 0:
	// must drop immediately in a single step without adding intermediate raw steps!
	beforeRawCount := len(setter.rawSnapshot())
	_, _ = io.WriteString(pw, "workspace>>2\n")
	select {
	case dimPct := <-setter.notify:
		if dimPct != 10 {
			t.Fatalf("dimPct = %d, want 10", dimPct)
		}
	case <-time.After(60 * time.Millisecond):
		t.Fatal("expected instant dim drop to 10%%")
	}
	if afterRawCount := len(setter.rawSnapshot()); afterRawCount != beforeRawCount {
		t.Errorf("dim_transition_ms=0 should not emit intermediate raw steps (before=%d, after=%d)", beforeRawCount, afterRawCount)
	}

	_ = pw.Close()
	if err := <-doneCh; err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}
}
