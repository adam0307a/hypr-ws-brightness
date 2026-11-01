// Package brightness wraps brightnessctl to discover backlight devices and
// read or set hardware screen brightness safely.
package brightness

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	// MinBrightness is the lowest allowed brightness percentage.
	MinBrightness = 0
	// MaxBrightness is the highest allowed brightness percentage.
	MaxBrightness = 100
)

// ErrBinaryNotFound indicates that brightnessctl is not installed or not in PATH.
var ErrBinaryNotFound = errors.New("brightnessctl executable not found in PATH")

// ErrNoBacklightDevice indicates that no backlight-class device was reported by brightnessctl.
var ErrNoBacklightDevice = errors.New("no backlight device found")

// DeviceInfo represents a single device entry reported by `brightnessctl -m`.
type DeviceInfo struct {
	Name       string
	Class      string
	Current    int
	Percentage int
	Max        int
}

// Runner abstracts command execution and binary lookup for testability.
type Runner interface {
	LookPath(file string) (string, error)
	Run(name string, args ...string) ([]byte, error)
}

// OSRunner executes real commands on the host system.
type OSRunner struct{}

// LookPath searches for an executable in PATH.
func (OSRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// Run executes a command and returns its combined stdout/stderr output.
func (OSRunner) Run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}

// Clamp restricts a brightness percentage to the valid [0, 100] range.
func Clamp(percent int) int {
	if percent < MinBrightness {
		return MinBrightness
	}
	if percent > MaxBrightness {
		return MaxBrightness
	}
	return percent
}

// PercentToRaw converts a [0, 100] brightness percentage to a raw device value in [0, max].
func PercentToRaw(percent, max int) int {
	clamped := Clamp(percent)
	if max <= 0 {
		return clamped
	}
	raw := (clamped*max + 50) / 100
	if raw < 0 {
		return 0
	}
	if raw > max {
		return max
	}
	return raw
}

// RawToPercent converts a raw device brightness value in [0, max] to a [0, 100] percentage.
func RawToPercent(raw, max int) int {
	if max <= 0 {
		return Clamp(raw)
	}
	if raw <= 0 {
		return 0
	}
	if raw >= max {
		return 100
	}
	return Clamp((raw*100 + max/2) / max)
}

// ParseMachineList parses the CSV output produced by `brightnessctl -m --list`
// or `brightnessctl -m -d <device>`.
// Expected format per line: device,class,current,percentage%,max
func ParseMachineList(output string) []DeviceInfo {
	var devices []DeviceInfo
	lines := strings.Split(output, "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 5 {
			continue
		}

		name := strings.TrimSpace(fields[0])
		class := strings.TrimSpace(fields[1])
		if name == "" || class == "" {
			continue
		}

		curr, _ := strconv.Atoi(strings.TrimSpace(fields[2]))
		maxVal, _ := strconv.Atoi(strings.TrimSpace(fields[4]))

		pctStr := strings.TrimSuffix(strings.TrimSpace(fields[3]), "%")
		pct, err := strconv.Atoi(pctStr)
		if err != nil && maxVal > 0 {
			pct = (curr * 100) / maxVal
		}

		devices = append(devices, DeviceInfo{
			Name:       name,
			Class:      class,
			Current:    curr,
			Percentage: Clamp(pct),
			Max:        maxVal,
		})
	}
	return devices
}

// BacklightDevices filters a device slice down to devices of class "backlight".
func BacklightDevices(devices []DeviceInfo) []DeviceInfo {
	var out []DeviceInfo
	for _, d := range devices {
		if d.Class == "backlight" {
			out = append(out, d)
		}
	}
	return out
}

// SelectDevice chooses a target brightness device using the priority order:
//  1. Explicit configured device (when not empty and not "auto")
//  2. First discovered "backlight" class device
//  3. Error if no backlight device exists
//
// It returns the chosen DeviceInfo, an optional non-fatal warning message, and any error.
func SelectDevice(configured string, devices []DeviceInfo) (DeviceInfo, string, error) {
	backlights := BacklightDevices(devices)
	target := strings.TrimSpace(configured)

	if target != "" && !strings.EqualFold(target, "auto") {
		for _, d := range backlights {
			if d.Name == target {
				return d, "", nil
			}
		}
		for _, d := range devices {
			if d.Name == target {
				return d, "", nil
			}
		}
		if len(backlights) > 0 {
			warn := fmt.Sprintf(
				"configured brightness device %q was not found; falling back to %q",
				target, backlights[0].Name,
			)
			return backlights[0], warn, nil
		}
		return DeviceInfo{}, "", fmt.Errorf(
			"%w: configured device %q was not found and no backlight devices are available",
			ErrNoBacklightDevice, target,
		)
	}

	if len(backlights) == 0 {
		return DeviceInfo{}, "", fmt.Errorf(
			"%w: no device of class 'backlight' reported by 'brightnessctl -m --list'",
			ErrNoBacklightDevice,
		)
	}

	if len(backlights) > 1 {
		names := make([]string, 0, len(backlights))
		for _, d := range backlights {
			names = append(names, d.Name)
		}
		note := fmt.Sprintf(
			"multiple backlight devices found (%s); selected %q (set 'device' in config.toml to override)",
			strings.Join(names, ", "), backlights[0].Name,
		)
		return backlights[0], note, nil
	}

	return backlights[0], "", nil
}

// CheckBinary verifies that brightnessctl is installed and accessible in PATH.
func CheckBinary(runner Runner) error {
	if runner == nil {
		runner = OSRunner{}
	}
	if _, err := runner.LookPath("brightnessctl"); err != nil {
		return fmt.Errorf(
			"%w: please install brightnessctl (e.g. 'sudo pacman -S brightnessctl' or 'sudo apt install brightnessctl')",
			ErrBinaryNotFound,
		)
	}
	return nil
}

// Controller manages brightness operations for a resolved backlight device.
type Controller struct {
	runner Runner
	device string
}

// NewController verifies brightnessctl availability, resolves the target backlight
// device, and returns a ready Controller. If warnFn is non-nil, non-fatal device
// selection warnings are passed to it.
func NewController(configuredDevice string, warnFn func(string)) (*Controller, error) {
	return NewControllerWithRunner(configuredDevice, OSRunner{}, warnFn)
}

// NewControllerWithRunner creates a Controller using a custom command Runner.
func NewControllerWithRunner(configuredDevice string, runner Runner, warnFn func(string)) (*Controller, error) {
	if runner == nil {
		runner = OSRunner{}
	}
	if err := CheckBinary(runner); err != nil {
		return nil, err
	}

	c := &Controller{runner: runner}
	if err := c.ResolveDevice(configuredDevice, warnFn); err != nil {
		return nil, err
	}
	return c, nil
}

// ResolveDevice queries brightnessctl for available devices and updates the active device.
func (c *Controller) ResolveDevice(configuredDevice string, warnFn func(string)) error {
	out, err := c.runner.Run("brightnessctl", "-m", "--list")
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("failed to list brightness devices: %w (%s)", err, msg)
		}
		return fmt.Errorf("failed to list brightness devices: %w", err)
	}

	devices := ParseMachineList(string(out))
	selected, warn, err := SelectDevice(configuredDevice, devices)
	if err != nil {
		return err
	}
	if warn != "" && warnFn != nil {
		warnFn(warn)
	}
	c.device = selected.Name
	return nil
}

// Device returns the resolved brightnessctl device name (e.g. "amdgpu_bl2").
func (c *Controller) Device() string {
	return c.device
}

// GetInfo reads full device metadata (current raw value, percentage, max) for the active device.
func (c *Controller) GetInfo() (DeviceInfo, error) {
	out, err := c.runner.Run("brightnessctl", "-m", "-d", c.device)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return DeviceInfo{}, fmt.Errorf("failed to read brightness from %q: %w (%s)", c.device, err, msg)
		}
		return DeviceInfo{}, fmt.Errorf("failed to read brightness from %q: %w", c.device, err)
	}

	devices := ParseMachineList(string(out))
	if len(devices) == 0 {
		return DeviceInfo{}, fmt.Errorf("unexpected brightnessctl output for device %q: %q", c.device, strings.TrimSpace(string(out)))
	}
	return devices[0], nil
}

// Get reads the current brightness percentage (0-100) from the active device.
func (c *Controller) Get() (int, error) {
	info, err := c.GetInfo()
	if err != nil {
		return 0, err
	}
	return Clamp(info.Percentage), nil
}

// Set applies the given brightness percentage (clamped to 0-100) to the active device
// and returns the clamped value that was applied.
func (c *Controller) Set(percent int) (int, error) {
	clamped := Clamp(percent)
	arg := fmt.Sprintf("%d%%", clamped)
	out, err := c.runner.Run("brightnessctl", "-q", "-d", c.device, "set", arg)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return clamped, fmt.Errorf("failed to set brightness on %q to %s: %w (%s)", c.device, arg, err, msg)
		}
		return clamped, fmt.Errorf("failed to set brightness on %q to %s: %w", c.device, arg, err)
	}
	return clamped, nil
}

// SetRaw applies a raw hardware brightness integer clamped to [0, max].
// If max <= 0, raw is treated as a [0, 100] percentage.
func (c *Controller) SetRaw(raw, max int) error {
	if max <= 0 {
		_, err := c.Set(raw)
		return err
	}
	if raw < 0 {
		raw = 0
	}
	if raw > max {
		raw = max
	}
	arg := strconv.Itoa(raw)
	out, err := c.runner.Run("brightnessctl", "-q", "-d", c.device, "set", arg)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("failed to set raw brightness on %q to %s: %w (%s)", c.device, arg, err, msg)
		}
		return fmt.Errorf("failed to set raw brightness on %q to %s: %w", c.device, arg, err)
	}
	return nil
}
