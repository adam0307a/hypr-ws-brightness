# hypr-ws-brightness

A lightweight, zero-runtime-dependency **per-workspace hardware brightness manager** for [Hyprland](https://hyprland.org/), complete with an event-driven background daemon and an interactive terminal UI (TUI).

Unlike window-dimming shaders (`hyprdim`) or ambient-light daemons (`wluma`), `hypr-ws-brightness` maps individual Hyprland workspaces directly to **hardware backlight levels** via `brightnessctl` — ideal when switching between a bright browser workspace, a dim terminal workspace, and a media workspace.

---

## Screenshot

<!-- Replace the placeholder below with an actual TUI screenshot before publishing -->
<img width="1779" height="669" alt="image" src="https://github.com/user-attachments/assets/40579b12-5b87-41aa-b473-30712a7598fb" />


---

## Features

- **Zero-polling IPC daemon:** Listens directly to Hyprland's `.socket2.sock` Unix socket (`workspacev2>>` and `workspace>>` events).
- **Duplicate event debouncing:** Suppresses consecutive duplicate events for the same workspace ID (100 ms default window) so paired `workspace` + `workspacev2` messages never invoke `brightnessctl` twice.
- **Smart backlight auto-detection:** Filters `brightnessctl -m --list` strictly for `backlight`-class devices (ignoring keyboard/NIC `leds`) with explicit override support (`configured > first backlight > error`).
- **Live configuration reload:** Edits made in the TUI (or manually in `config.toml`) are picked up automatically by the running daemon without a restart.
- **Interactive TUI:** Adjust per-workspace and fallback brightness with arrow keys or direct numeric input, preview live on hardware, or **capture** your current screen brightness into a workspace with a single keystroke (`c`).
- **Fault-tolerant configuration:** Corrupt or out-of-range `config.toml` files never crash the daemon — values are clamped to `0–100%` and corrupt files safely fall back to defaults with clear warnings.

---

## Requirements

- **Window Manager:** Hyprland (Wayland)
- **Backlight Utility:** [`brightnessctl`](https://github.com/Hummer12007/brightnessctl)
  - Arch Linux: `sudo pacman -S brightnessctl`
  - Debian / Ubuntu: `sudo apt install brightnessctl`
  - Fedora: `sudo dnf install brightnessctl`
  - *(Ensure your user has permission to change backlight brightness — usually via `systemd-logind` / `seatd` or the `video` group.)*
- **Build Toolchain:** Go `1.24+` and `make` (only needed to build from source)

---

## Installation

### Option 1: Arch Linux (`makepkg` / `PKGBUILD`)

Install as a native `pacman`-tracked package using the included [`PKGBUILD`](packaging/aur/PKGBUILD):

```bash
git clone https://github.com/adam0307a/hypr-ws-brightness.git
cd hypr-ws-brightness/packaging/aur
makepkg -si
```

*(Can be cleanly removed anytime with `sudo pacman -R hypr-ws-brightness-bin`.)*

### Option 2: Pre-built Static Binary (All Linux Distros)

Download the latest static archive for your architecture from [Releases](https://github.com/adam0307a/hypr-ws-brightness/releases) and place `hypr-ws-brightness` into `~/.local/bin` or `/usr/local/bin`:

```bash
tar -xzf hypr-ws-brightness-1.1.0-linux-amd64.tar.gz
install -Dm755 hypr-ws-brightness ~/.local/bin/hypr-ws-brightness
```

### Option 3: Build & Install from Source

```bash
git clone https://github.com/adam0307a/hypr-ws-brightness.git
cd hypr-ws-brightness

make build
make install   # Installs binary to ~/.local/bin/hypr-ws-brightness
```

Or install directly via the Go toolchain:

```bash
go install github.com/adam0307a/hypr-ws-brightness/cmd/hypr-ws-brightness@latest
```

*(Ensure `~/.local/bin` is in your `$PATH`, or install system-wide with `sudo make install PREFIX=/usr/local`.)*

---

## Quick Start

1. **Initialize your configuration:**

   ```bash
   hypr-ws-brightness init
   ```

   This creates `~/.config/hypr-ws-brightness/config.toml` (if it does not exist yet), verifies your backlight device, and prints the `exec-once` snippet for Hyprland.

2. **Autostart the daemon in Hyprland:**

   **Classic configuration (`~/.config/hypr/hyprland.conf`):**

   ```ini
   exec-once = hypr-ws-brightness daemon

   # Optional keybinding to open the TUI:
   bind = $mainMod SHIFT, B, exec, kitty -e hypr-ws-brightness tui
   ```

   **Lua configuration — Hyprland 0.57+ (`~/.config/hypr/hyprland.lua`):**

   ```lua
   hl.on("hyprland.start", function()
       hl.exec_cmd("hypr-ws-brightness daemon")
   end)

   -- Optional keybinding to open the TUI:
   hl.bind("SUPER + SHIFT + B", hl.dsp.exec_cmd("kitty -e hypr-ws-brightness tui"))
   ```

3. **Customize workspace brightness levels interactively:**

   ```bash
   hypr-ws-brightness tui
   ```

---

## Usage

```text
hypr-ws-brightness <command> [flags]

Commands:
  daemon    Listen to Hyprland workspace events and apply configured brightness
  tui       Open interactive terminal UI to edit per-workspace brightness
  init      Create a sample config.toml and print Hyprland exec-once instructions
  version   Print version information
  help      Show help message
```

### Daemon Mode

Runs silently in the background by default, logging only warnings or errors to `stderr`:

```bash
# Standard quiet mode (for exec-once)
hypr-ws-brightness daemon

# Verbose logging for debugging workspace transitions
hypr-ws-brightness daemon --verbose

# Custom config file, explicit device override, or custom debounce window
hypr-ws-brightness daemon --config /path/to/config.toml --device amdgpu_bl2 --debounce-ms 100
```

### TUI Mode

```bash
hypr-ws-brightness tui
```

#### TUI Keybindings

| Key | Action |
| :--- | :--- |
| `↑` / `↓` or `k` / `j` | Select workspace, `Default`, `Brighten Delay`, or `Smooth Transition` row |
| `←` / `→` or `h` / `l` | Adjust brightness by `±5%` (or delay / fade duration by `±100 ms`) |
| `-` / `+` or `[` / `]` | Fine-tune brightness by `±1%` (or delay / fade duration by `±50 ms`) |
| `0–9`, `Enter`, or `e` | Type an exact percentage (`0–100`) or duration in ms |
| `i` | Toggle **`⚡ 0ms delay`** override on the selected workspace |
| `y` / `f` | Jump directly to editing `Brighten Delay` (`y`) or `Smooth Transition` (`f`) |
| `c` | **Capture** current hardware brightness and save to selected workspace |
| `p` or `Space` | Immediately apply/preview selected row's brightness on hardware |
| `t` | Toggle live hardware preview across all rows vs. active workspace only |
| `a` | Add a new workspace ID not currently open |
| `d`, `x`, or `Backspace` | Remove custom rule for workspace (revert to `default_brightness`) |
| `r` | Refresh workspace list and current hardware brightness |
| `D` (Shift+d) | **Start / stop the background daemon** (e.g. pause it to adjust brightness from your shell/bar UI without it being overridden on the next workspace switch) |
| `R` (Shift+r) | Restart the daemon (also collapses duplicate instances into one) |
| `s` | Explicitly save configuration to disk |
| `q` or `Esc` | Quit TUI |

---

## Configuration

Default path: `~/.config/hypr-ws-brightness/config.toml` (respects `$XDG_CONFIG_HOME`).

```toml
# Backlight device name from `brightnessctl --list` (e.g. "amdgpu_bl2", "intel_backlight")
# Use "auto" (or leave empty) to automatically pick the first backlight-class device.
device = "auto"

# Fallback brightness percentage (0-100) for workspaces without an explicit rule
default_brightness = 50

# Delay in milliseconds (0-30000) before brightening (low -> high).
# Dimming (high -> low) always begins immediately (0ms delay).
delay_ms = 500

# Slow, gentle fade duration in ms (0-10000) when brightening (low -> high).
transition_ms = 800

# Fast/instant dim duration in ms (0-10000) when dimming (high -> low; 0 = instant drop).
dim_transition_ms = 100

# Workspace IDs that always bypass delay_ms (0ms delay even when brightening).
instant_workspaces = []

[workspaces]
1 = 70
2 = 40
3 = 30
```

### Backlight Device Selection Priority

When multiple devices exist (`amdgpu_bl0`, `amdgpu_bl1`, keyboard LEDs, etc.):

1. **Configured device:** If `device` in `config.toml` (or `--device`) is set to a specific name (not `"auto"`), that device is selected. If the named device is missing, `hypr-ws-brightness` warns and falls back to the first available backlight.
2. **First `backlight`-class device:** In `"auto"` mode, the first device of class `backlight` from `brightnessctl -m --list` is selected (devices of class `leds` are always ignored).
3. **Error:** If no `backlight` device exists on the system, a clear error is reported.

---

## Development & Testing

```bash
# Run unit tests with the race detector
make test

# Run go vet
make vet

# Build binary
make build
```

---

## License

Released under the [MIT License](LICENSE).
