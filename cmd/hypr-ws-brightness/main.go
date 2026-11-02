// Command hypr-ws-brightness manages per-workspace hardware screen brightness on Hyprland.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adam0307a/hypr-ws-brightness/internal/brightness"
	"github.com/adam0307a/hypr-ws-brightness/internal/config"
	"github.com/adam0307a/hypr-ws-brightness/internal/daemon"
	"github.com/adam0307a/hypr-ws-brightness/internal/hyprland"
	"github.com/adam0307a/hypr-ws-brightness/internal/tui"
)

var version = "1.1.0"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var globalConfig string
	var globalDevice string
	var globalVerbose bool

	// Consume optional leading flags placed before the subcommand.
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-h", "--help":
			printUsage(stdout)
			return 0
		case "-V", "--version":
			fmt.Fprintf(stdout, "hypr-ws-brightness %s\n", version)
			return 0
		case "-v", "--verbose":
			globalVerbose = true
			args = args[1:]
		case "--config":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "Error: --config requires a file path argument")
				return 2
			}
			globalConfig = args[1]
			args = args[2:]
		case "--device":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "Error: --device requires a device name argument")
				return 2
			}
			globalDevice = args[1]
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "--config=") {
				globalConfig = strings.TrimPrefix(args[0], "--config=")
				args = args[1:]
			} else if strings.HasPrefix(args[0], "--device=") {
				globalDevice = strings.TrimPrefix(args[0], "--device=")
				args = args[1:]
			} else {
				fmt.Fprintf(stderr, "Error: unknown flag %q\n\n", args[0])
				printUsage(stderr)
				return 2
			}
		}
	}

	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "daemon":
		return runDaemon(subArgs, globalConfig, globalDevice, globalVerbose, stderr)
	case "tui":
		return runTUI(subArgs, globalConfig, globalDevice, stderr)
	case "init":
		return runInit(subArgs, globalConfig, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "hypr-ws-brightness %s\n", version)
		return 0
	case "help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "Error: unknown command %q\n\n", subcmd)
		printUsage(stderr)
		return 2
	}
}

func runDaemon(args []string, defConfig, defDevice string, defVerbose bool, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfgPath := fs.String("config", defConfig, "Path to config.toml (default: ~/.config/hypr-ws-brightness/config.toml)")
	device := fs.String("device", defDevice, "Override brightnessctl device name")
	debounceMs := fs.Int("debounce-ms", 100, "Debounce window in milliseconds for consecutive identical workspace IDs")
	delayMs := fs.Int("delay-ms", -1, "Override brighten delay in milliseconds (low->high only; high->low is always 0ms)")
	transitionMs := fs.Int("transition-ms", -1, "Override smooth fade duration in milliseconds (default: use transition_ms from config.toml)")
	verbose := fs.Bool("verbose", defVerbose, "Enable verbose event and brightness logging")
	fs.BoolVar(verbose, "v", defVerbose, "Enable verbose event and brightness logging (shorthand)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	var delayOverride *time.Duration
	if *delayMs >= 0 {
		dur := time.Duration(config.ClampDelay(*delayMs)) * time.Millisecond
		delayOverride = &dur
	}

	var transitionOverride *time.Duration
	if *transitionMs >= 0 {
		dur := time.Duration(config.ClampTransition(*transitionMs)) * time.Millisecond
		transitionOverride = &dur
	}

	d, err := daemon.New(daemon.Options{
		ConfigPath:         *cfgPath,
		DeviceOverride:     *device,
		DebounceWindow:     time.Duration(*debounceMs) * time.Millisecond,
		DelayOverride:      delayOverride,
		TransitionOverride: transitionOverride,
		Verbose:            *verbose,
		Stderr:             stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func runTUI(args []string, defConfig, defDevice string, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfgPath := fs.String("config", defConfig, "Path to config.toml (default: ~/.config/hypr-ws-brightness/config.toml)")
	device := fs.String("device", defDevice, "Override brightnessctl device name")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if err := tui.Run(tui.Options{
		ConfigPath:     *cfgPath,
		DeviceOverride: *device,
		Stderr:         stderr,
	}); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func runInit(args []string, defConfig string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfgPath := fs.String("config", defConfig, "Path to config.toml (default: ~/.config/hypr-ws-brightness/config.toml)")
	force := fs.Bool("force", false, "Overwrite existing configuration file if present")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	targetPath := strings.TrimSpace(*cfgPath)
	if targetPath == "" {
		var err error
		targetPath, err = config.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
	}

	if !hyprland.InSession() {
		fmt.Fprintln(stderr, "Warning: HYPRLAND_INSTANCE_SIGNATURE is not set (currently running outside Hyprland).")
	}

	if err := brightness.CheckBinary(nil); err != nil {
		fmt.Fprintf(stderr, "Warning: %v\n", err)
	} else if ctrl, err := brightness.NewController("auto", nil); err == nil {
		fmt.Fprintf(stdout, "Detected backlight device: %s\n", ctrl.Device())
	}

	_, statErr := os.Stat(targetPath)
	fileExists := statErr == nil

	if fileExists && !*force {
		fmt.Fprintf(stdout, "Configuration file already exists: %s\n", targetPath)
		fmt.Fprintln(stdout, "(Pass --force to overwrite with the default example configuration.)")
	} else {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			fmt.Fprintf(stderr, "Error: failed to create directory %q: %v\n", filepath.Dir(targetPath), err)
			return 1
		}
		if err := os.WriteFile(targetPath, []byte(config.SampleTOML()), 0o644); err != nil {
			fmt.Fprintf(stderr, "Error: failed to write %q: %v\n", targetPath, err)
			return 1
		}
		fmt.Fprintf(stdout, "Created sample configuration file: %s\n", targetPath)
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Add one of the following to your Hyprland configuration:")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "  Classic config (~/.config/hypr/hyprland.conf):")
	fmt.Fprintln(stdout, "    exec-once = hypr-ws-brightness daemon")
	fmt.Fprintln(stdout, "    bind = $mainMod SHIFT, B, exec, kitty -e hypr-ws-brightness tui")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "  Lua config — Hyprland 0.57+ (~/.config/hypr/hyprland.lua):")
	fmt.Fprintln(stdout, "    hl.on(\"hyprland.start\", function()")
	fmt.Fprintln(stdout, "        hl.exec_cmd(\"hypr-ws-brightness daemon\")")
	fmt.Fprintln(stdout, "    end)")
	fmt.Fprintln(stdout, "")
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `hypr-ws-brightness %s — Per-workspace hardware brightness manager for Hyprland

Usage:
  hypr-ws-brightness <command> [flags]

Commands:
  daemon    Listen to Hyprland workspace events and apply configured brightness
  tui       Open interactive terminal UI to edit per-workspace brightness
  init      Create a sample config.toml and print Hyprland exec-once instructions
  version   Print version information
  help      Show this help message

Daemon Flags:
  --config <path>         Path to config.toml (default: ~/.config/hypr-ws-brightness/config.toml)
  --device <name>         Override brightnessctl backlight device (default: from config or "auto")
  --debounce-ms <int>     Debounce window in ms for duplicate workspace events (default: 100)
  --delay-ms <int>        Override brighten delay in ms (low->high wait; dimming is always 0ms)
  --transition-ms <int>   Override smooth fade duration in ms (0 = sharp step)
  -v, --verbose           Log workspace events and brightness transitions to stderr

TUI Flags:
  --config <path>       Path to config.toml
  --device <name>       Override brightnessctl backlight device

Init Flags:
  --config <path>       Target path for generated config.toml
  --force               Overwrite existing config.toml
`, version)
}
