package brightness

import (
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	missingBin bool
	outputs    map[string]string
	calls      [][]string
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if f.missingBin {
		return "", errors.New("not found")
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	key := strings.Join(call, " ")
	if out, ok := f.outputs[key]; ok {
		return []byte(out), nil
	}
	return nil, nil
}

func TestClamp(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-50, 0},
		{-1, 0},
		{0, 0},
		{42, 42},
		{100, 100},
		{101, 100},
		{999, 100},
	}

	for _, tc := range cases {
		if got := Clamp(tc.in); got != tc.want {
			t.Errorf("Clamp(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseMachineListAndSelectDevice(t *testing.T) {
	raw := `
enp2s0-0::lan,leds,0,0%,255
amdgpu_bl1,backlight,19661,30%,65535
amdgpu_bl2,backlight,32768,50%,65535
input2::capslock,leds,1,100%,1
`
	devices := ParseMachineList(raw)
	if len(devices) != 4 {
		t.Fatalf("ParseMachineList returned %d devices, want 4", len(devices))
	}

	backlights := BacklightDevices(devices)
	if len(backlights) != 2 {
		t.Fatalf("BacklightDevices returned %d devices, want 2", len(backlights))
	}

	// Auto mode selects first backlight and warns when multiple backlights exist.
	dev, warn, err := SelectDevice("auto", devices)
	if err != nil {
		t.Fatalf("SelectDevice(auto) unexpected error: %v", err)
	}
	if dev.Name != "amdgpu_bl1" {
		t.Errorf("SelectDevice(auto) selected %q, want amdgpu_bl1", dev.Name)
	}
	if warn == "" {
		t.Errorf("expected multi-backlight selection note, got empty warning")
	}

	// Explicit device takes precedence over first found.
	dev, warn, err = SelectDevice("amdgpu_bl2", devices)
	if err != nil {
		t.Fatalf("SelectDevice(amdgpu_bl2) unexpected error: %v", err)
	}
	if dev.Name != "amdgpu_bl2" {
		t.Errorf("SelectDevice(amdgpu_bl2) selected %q, want amdgpu_bl2", dev.Name)
	}
	if warn != "" {
		t.Errorf("expected no warning for exact match, got %q", warn)
	}

	// Unknown configured device falls back to first backlight with warning.
	dev, warn, err = SelectDevice("intel_backlight", devices)
	if err != nil {
		t.Fatalf("SelectDevice(intel_backlight) unexpected error: %v", err)
	}
	if dev.Name != "amdgpu_bl1" {
		t.Errorf("fallback selected %q, want amdgpu_bl1", dev.Name)
	}
	if !strings.Contains(warn, "intel_backlight") {
		t.Errorf("expected warning mentioning missing device, got %q", warn)
	}

	// Only LED devices -> error.
	ledsOnly := ParseMachineList("input2::numlock,leds,0,0%,1\n")
	if _, _, err := SelectDevice("auto", ledsOnly); !errors.Is(err, ErrNoBacklightDevice) {
		t.Errorf("expected ErrNoBacklightDevice when no backlights exist, got %v", err)
	}
}

func TestControllerMissingBinary(t *testing.T) {
	runner := &fakeRunner{missingBin: true}
	_, err := NewControllerWithRunner("auto", runner, nil)
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}
}

func TestControllerGetAndSetClamping(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			"brightnessctl -m --list":        "amdgpu_bl2,backlight,32768,50%,65535\n",
			"brightnessctl -m -d amdgpu_bl2": "amdgpu_bl2,backlight,45875,70%,65535\n",
		},
	}

	ctrl, err := NewControllerWithRunner("auto", runner, nil)
	if err != nil {
		t.Fatalf("NewControllerWithRunner failed: %v", err)
	}
	if ctrl.Device() != "amdgpu_bl2" {
		t.Fatalf("Device() = %q, want amdgpu_bl2", ctrl.Device())
	}

	got, err := ctrl.Get()
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	if got != 70 {
		t.Errorf("Get() = %d, want 70", got)
	}

	applied, err := ctrl.Set(145)
	if err != nil {
		t.Fatalf("Set(145) failed: %v", err)
	}
	if applied != 100 {
		t.Errorf("Set(145) applied %d, want 100", applied)
	}

	lastCall := strings.Join(runner.calls[len(runner.calls)-1], " ")
	wantCall := "brightnessctl -q -d amdgpu_bl2 set 100%"
	if lastCall != wantCall {
		t.Errorf("last command = %q, want %q", lastCall, wantCall)
	}

	if raw := PercentToRaw(50, 65535); raw != 32768 {
		t.Errorf("PercentToRaw(50, 65535) = %d, want 32768", raw)
	}
	if pct := RawToPercent(32768, 65535); pct != 50 {
		t.Errorf("RawToPercent(32768, 65535) = %d, want 50", pct)
	}

	if err := ctrl.SetRaw(90000, 65535); err != nil {
		t.Fatalf("SetRaw failed: %v", err)
	}
	lastCall = strings.Join(runner.calls[len(runner.calls)-1], " ")
	if lastCall != "brightnessctl -q -d amdgpu_bl2 set 65535" {
		t.Errorf("SetRaw clamped call = %q, want 65535", lastCall)
	}
}
