// Package service discovers, starts, and stops running hypr-ws-brightness
// daemon processes so the TUI can toggle the background daemon on demand
// (e.g. to temporarily hand brightness control back to the desktop shell).
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// BinaryName is the executable basename used to recognise daemon processes.
const BinaryName = "hypr-ws-brightness"

// procRoot is overridable in tests.
var procRoot = "/proc"

// IsDaemonCmdline reports whether the NUL-split argv belongs to a
// "hypr-ws-brightness daemon" process.
func IsDaemonCmdline(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	if filepath.Base(argv[0]) != BinaryName {
		return false
	}
	for _, a := range argv[1:] {
		if a == "daemon" {
			return true
		}
	}
	return false
}

// FindDaemons returns the sorted PIDs of running daemon processes owned by
// the current user, excluding the calling process.
func FindDaemons() ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", procRoot, err)
	}
	self := os.Getpid()
	uid := os.Getuid()

	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		if info, err := os.Stat(dir); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
				continue
			}
		}
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if IsDaemonCmdline(argv) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Stop sends SIGTERM to every running daemon, waits up to timeout for them
// to exit, and escalates to SIGKILL for any stragglers. It returns the PIDs
// that were signalled.
func Stop(timeout time.Duration) ([]int, error) {
	pids, err := FindDaemons()
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, nil
	}
	var errs []error
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, fmt.Errorf("pid %d: %w", pid, err))
		}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := false
		for _, pid := range pids {
			if alive(pid) {
				remaining = true
				break
			}
		}
		if !remaining {
			return pids, errors.Join(errs...)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range pids {
		if alive(pid) {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				errs = append(errs, fmt.Errorf("pid %d: %w", pid, err))
			}
		}
	}
	return pids, errors.Join(errs...)
}

// StartOptions configures a detached daemon launch.
type StartOptions struct {
	// ConfigPath is forwarded as --config when non-empty.
	ConfigPath string
	// Device is forwarded as --device when non-empty.
	Device string
}

// Start launches a new detached "hypr-ws-brightness daemon" process in its
// own session so it survives the TUI and its terminal being closed.
func Start(opts StartOptions) (int, error) {
	exe, err := os.Executable()
	if err != nil || filepath.Base(exe) != BinaryName {
		if p, lookErr := exec.LookPath(BinaryName); lookErr == nil {
			exe = p
		} else if err != nil {
			return 0, fmt.Errorf("cannot locate %s binary: %w", BinaryName, err)
		}
	}

	args := []string{"daemon"}
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}
	if opts.Device != "" {
		args = append(args, "--device", opts.Device)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start daemon: %w", err)
	}
	pid := cmd.Process.Pid
	// Reap the child in the background if it exits while the TUI is still
	// open, so it does not linger as a zombie.
	go func() { _ = cmd.Wait() }()

	// Give it a moment to fail fast (e.g. not inside Hyprland).
	time.Sleep(150 * time.Millisecond)
	if !alive(pid) || isZombie(pid) {
		return 0, errors.New("daemon exited immediately (is Hyprland running? try 'hypr-ws-brightness daemon -v')")
	}
	return pid, nil
}

func isZombie(pid int) bool {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	s := string(raw)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] == 'Z'
	}
	return false
}
