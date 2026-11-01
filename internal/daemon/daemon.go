// Package daemon listens to Hyprland's IPC socket2 stream and applies
// workspace-specific screen brightness levels via brightnessctl with
// direction-aware delays and smooth hardware transitions.
package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"time"

	"github.com/adam0307a/hypr-ws-brightness/internal/brightness"
	"github.com/adam0307a/hypr-ws-brightness/internal/config"
	"github.com/adam0307a/hypr-ws-brightness/internal/hyprland"
)

const (
	// DefaultDebounceWindow suppresses consecutive duplicate events for the same workspace ID.
	DefaultDebounceWindow = 100 * time.Millisecond
	// DefaultFadeInterval is the tick interval (~40 FPS) used during smooth brightness fades.
	DefaultFadeInterval = 25 * time.Millisecond
)

// BrightnessSetter abstracts brightness hardware operations for testing.
type BrightnessSetter interface {
	Device() string
	GetInfo() (brightness.DeviceInfo, error)
	Set(percent int) (int, error)
	SetRaw(raw, max int) error
	ResolveDevice(configuredDevice string, warnFn func(string)) error
}

// WorkspaceQuerier abstracts querying the active workspace from Hyprland.
type WorkspaceQuerier interface {
	ActiveWorkspace() (hyprland.Workspace, error)
}

// Debouncer filters out consecutive events targeting the same workspace ID
// within a configurable time window (preventing double execution when Hyprland
// emits both `workspace>>N` and `workspacev2>>N,N`).
type Debouncer struct {
	window   time.Duration
	now      func() time.Time
	hasLast  bool
	lastID   int
	lastTime time.Time
}

// NewDebouncer creates a Debouncer with the specified suppression window.
func NewDebouncer(window time.Duration) *Debouncer {
	if window < 0 {
		window = 0
	}
	return &Debouncer{
		window: window,
		now:    time.Now,
	}
}

// ShouldProcess returns true if an event for workspaceID should be handled now,
// or false if the same workspaceID was already processed within the debounce window.
func (d *Debouncer) ShouldProcess(workspaceID int) bool {
	now := d.now()
	if d.hasLast && d.lastID == workspaceID && now.Sub(d.lastTime) < d.window {
		return false
	}
	d.hasLast = true
	d.lastID = workspaceID
	d.lastTime = now
	return true
}

// Options configures a Daemon instance.
type Options struct {
	ConfigPath            string
	DeviceOverride        string
	DebounceWindow        time.Duration
	DelayOverride         *time.Duration
	TransitionOverride    *time.Duration
	DimTransitionOverride *time.Duration
	FadeInterval          time.Duration
	Verbose               bool
	Stderr                io.Writer
}

type fileStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

// Daemon coordinates Hyprland IPC event consumption, config reloading,
// asymmetric brighten delays, and smooth brightness transitions.
type Daemon struct {
	opts       Options
	setter     BrightnessSetter
	querier    WorkspaceQuerier
	debouncer  *Debouncer
	cfg        *config.Config
	cfgStamp   fileStamp
	infoLogger *log.Logger
	warnLogger *log.Logger

	hasState bool
	curPct   int
	curRaw   int
	maxRaw   int
}

// New creates and initializes a Daemon, verifying the Hyprland session and backlight device.
func New(opts Options) (*Daemon, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.DebounceWindow <= 0 {
		opts.DebounceWindow = DefaultDebounceWindow
	}
	if opts.FadeInterval <= 0 {
		opts.FadeInterval = DefaultFadeInterval
	}
	if opts.ConfigPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return nil, err
		}
		opts.ConfigPath = p
	}

	if _, err := hyprland.InstanceSignature(); err != nil {
		return nil, err
	}

	warnLogger := log.New(opts.Stderr, "hypr-ws-brightness [WARN] ", log.LstdFlags)
	infoOut := io.Discard
	if opts.Verbose {
		infoOut = opts.Stderr
	}
	infoLogger := log.New(infoOut, "hypr-ws-brightness [INFO] ", log.LstdFlags)

	d := &Daemon{
		opts:       opts,
		querier:    hyprland.NewClient(),
		debouncer:  NewDebouncer(opts.DebounceWindow),
		cfg:        config.Default(),
		infoLogger: infoLogger,
		warnLogger: warnLogger,
	}

	d.loadConfigInitial()

	targetDevice := d.effectiveConfiguredDevice()
	ctrl, err := brightness.NewController(targetDevice, func(w string) {
		d.warnLogger.Println(w)
	})
	if err != nil {
		return nil, err
	}
	d.setter = ctrl
	d.syncHardwareState()

	return d, nil
}

// NewWithDeps constructs a Daemon with injected dependencies for unit tests.
func NewWithDeps(
	opts Options,
	setter BrightnessSetter,
	querier WorkspaceQuerier,
	debouncer *Debouncer,
) *Daemon {
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.DebounceWindow <= 0 {
		opts.DebounceWindow = DefaultDebounceWindow
	}
	if opts.FadeInterval <= 0 {
		opts.FadeInterval = DefaultFadeInterval
	}
	if debouncer == nil {
		debouncer = NewDebouncer(opts.DebounceWindow)
	}
	warnLogger := log.New(opts.Stderr, "hypr-ws-brightness [WARN] ", 0)
	infoOut := io.Discard
	if opts.Verbose {
		infoOut = opts.Stderr
	}
	infoLogger := log.New(infoOut, "hypr-ws-brightness [INFO] ", 0)

	d := &Daemon{
		opts:       opts,
		setter:     setter,
		querier:    querier,
		debouncer:  debouncer,
		cfg:        config.Default(),
		infoLogger: infoLogger,
		warnLogger: warnLogger,
	}
	if opts.ConfigPath != "" {
		d.loadConfigInitial()
	}
	d.syncHardwareState()
	return d
}

func (d *Daemon) syncHardwareState() {
	if d.setter == nil {
		return
	}
	info, err := d.setter.GetInfo()
	if err != nil || info.Max <= 0 {
		return
	}
	d.maxRaw = info.Max
	d.curRaw = info.Current
	d.curPct = brightness.Clamp(info.Percentage)
	d.hasState = true
}

func (d *Daemon) effectiveConfiguredDevice() string {
	if d.opts.DeviceOverride != "" {
		return d.opts.DeviceOverride
	}
	return d.cfg.Device
}

func (d *Daemon) effectiveDelay() time.Duration {
	if d.opts.DelayOverride != nil {
		if *d.opts.DelayOverride < 0 {
			return 0
		}
		return *d.opts.DelayOverride
	}
	return time.Duration(config.ClampDelay(d.cfg.DelayMs)) * time.Millisecond
}

func (d *Daemon) effectiveTransition() time.Duration {
	if d.opts.TransitionOverride != nil {
		if *d.opts.TransitionOverride < 0 {
			return 0
		}
		return *d.opts.TransitionOverride
	}
	return time.Duration(config.ClampTransition(d.cfg.TransitionMs)) * time.Millisecond
}

func (d *Daemon) effectiveDimTransition() time.Duration {
	if d.opts.DimTransitionOverride != nil {
		if *d.opts.DimTransitionOverride < 0 {
			return 0
		}
		return *d.opts.DimTransitionOverride
	}
	return time.Duration(config.ClampTransition(d.cfg.DimTransitionMs)) * time.Millisecond
}

func currentStamp(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{exists: false}
	}
	return fileStamp{
		exists:  true,
		modTime: info.ModTime(),
		size:    info.Size(),
	}
}

func (d *Daemon) loadConfigInitial() {
	d.cfgStamp = currentStamp(d.opts.ConfigPath)
	res := config.Load(d.opts.ConfigPath)
	d.cfg = res.Config

	for _, w := range res.Warnings {
		d.warnLogger.Println(w)
	}
	if !res.Exists {
		d.infoLogger.Printf(
			"config file %q not found; using defaults (brightness=%d%%, transition=%dms)",
			d.opts.ConfigPath, d.cfg.DefaultBrightness, d.cfg.TransitionMs,
		)
	} else if !res.Corrupt {
		d.infoLogger.Printf(
			"loaded config %q (default=%d%%, delay=%dms, transition=%dms, rules=%d)",
			d.opts.ConfigPath, d.cfg.DefaultBrightness, d.cfg.DelayMs, d.cfg.TransitionMs, len(d.cfg.Workspaces),
		)
	}
}

func (d *Daemon) reloadConfigIfChanged() {
	if d.opts.ConfigPath == "" {
		return
	}
	stamp := currentStamp(d.opts.ConfigPath)
	if stamp.exists == d.cfgStamp.exists &&
		stamp.size == d.cfgStamp.size &&
		stamp.modTime.Equal(d.cfgStamp.modTime) {
		return
	}

	prevDevice := d.effectiveConfiguredDevice()
	d.cfgStamp = stamp
	res := config.Load(d.opts.ConfigPath)
	d.cfg = res.Config

	for _, w := range res.Warnings {
		d.warnLogger.Println(w)
	}
	d.infoLogger.Printf(
		"reloaded config %q (default=%d%%, delay=%dms, transition=%dms, rules=%d)",
		d.opts.ConfigPath, d.cfg.DefaultBrightness, d.cfg.DelayMs, d.cfg.TransitionMs, len(d.cfg.Workspaces),
	)

	nextDevice := d.effectiveConfiguredDevice()
	if nextDevice != prevDevice && d.setter != nil {
		if err := d.setter.ResolveDevice(nextDevice, func(w string) {
			d.warnLogger.Println(w)
		}); err != nil {
			d.warnLogger.Printf("failed to switch brightness device to %q: %v", nextDevice, err)
		} else {
			d.syncHardwareState()
		}
	}
}

// Run connects to Hyprland's socket2 and processes workspace events until ctx is cancelled
// or Hyprland closes the IPC socket.
func (d *Daemon) Run(ctx context.Context) error {
	sockPath, err := hyprland.Socket2Path()
	if err != nil {
		return err
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("failed to connect to Hyprland socket2 at %q: %w", sockPath, err)
	}
	defer conn.Close()

	d.infoLogger.Printf(
		"connected to %s (device=%s, debounce=%s, delay=%s, transition=%s)",
		sockPath, d.setter.Device(), d.opts.DebounceWindow, d.effectiveDelay(), d.effectiveTransition(),
	)

	// Apply brightness for the currently active workspace immediately on startup.
	if d.querier != nil {
		if ws, err := d.querier.ActiveWorkspace(); err == nil && ws.ID > 0 {
			if d.debouncer.ShouldProcess(ws.ID) {
				d.applyImmediate(ws.ID, "startup")
			}
		} else if err != nil {
			d.infoLogger.Printf("unable to query initial active workspace: %v", err)
		}
	}

	// Unblock reader immediately when ctx is cancelled.
	stopDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopDone:
		}
	}()
	defer close(stopDone)

	err = d.ProcessStream(ctx, conn)
	if ctx.Err() != nil {
		d.infoLogger.Println("received shutdown signal; stopping daemon")
		return nil
	}
	return err
}

// ProcessStream reads newline-delimited Hyprland IPC events from r and applies brightness rules.
// Dimming (high -> low) and instant_workspaces begin immediately (0ms delay), whereas brightening
// (low -> high) waits delay_ms before gliding smoothly over transition_ms.
func (d *Daemon) ProcessStream(ctx context.Context, r io.Reader) error {
	lineCh := make(chan string)
	errCh := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(r)
		// Allow up to 1 MiB per line so long window titles on socket2 never cause bufio.ErrTooLong.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			text := scanner.Text()
			select {
			case lineCh <- text:
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		}
	}()

	var (
		applyTimer    *time.Timer
		applyCh       <-chan time.Time
		pendingWS     int
		pendingSource string

		fadeTicker    *time.Ticker
		fadeCh        <-chan time.Time
		fadeStartRaw  int
		fadeTargetRaw int
		fadeTargetPct int
		fadeWS        int
		fadeSource    string
		fadeIsCustom  bool
		fadeIsDimming bool
		fadeStartTime time.Time
		fadeDuration  time.Duration
		lastSentRaw   int
	)

	cancelPending := func() {
		if applyTimer != nil {
			if !applyTimer.Stop() {
				select {
				case <-applyTimer.C:
				default:
				}
			}
		}
		applyCh = nil
	}
	defer cancelPending()

	stopFade := func() {
		if fadeTicker != nil {
			fadeTicker.Stop()
		}
		fadeCh = nil
	}
	defer stopFade()

	startTransition := func(wsID int, source string) {
		targetPct, isCustom := d.cfg.BrightnessFor(wsID)

		if !d.hasState || d.maxRaw <= 0 {
			stopFade()
			d.applyImmediate(wsID, source)
			return
		}

		targetRaw := brightness.PercentToRaw(targetPct, d.maxRaw)
		if d.curRaw == targetRaw && d.curPct == targetPct {
			stopFade()
			d.infoLogger.Printf("workspace %d already at %d%%; skipping transition", wsID, targetPct)
			return
		}

		isDimming := targetRaw < d.curRaw
		transDur := d.effectiveTransition()
		if isDimming {
			transDur = d.effectiveDimTransition()
		}

		if transDur <= 0 {
			stopFade()
			d.applyImmediate(wsID, source)
			return
		}

		fadeStartRaw = d.curRaw
		fadeTargetRaw = targetRaw
		fadeTargetPct = targetPct
		fadeWS = wsID
		fadeSource = source
		fadeIsCustom = isCustom
		fadeIsDimming = isDimming
		fadeStartTime = time.Now()
		fadeDuration = transDur
		lastSentRaw = d.curRaw

		if fadeTicker == nil {
			fadeTicker = time.NewTicker(d.opts.FadeInterval)
		} else {
			fadeTicker.Reset(d.opts.FadeInterval)
		}
		fadeCh = fadeTicker.C

		dirLabel := "brightening"
		if isDimming {
			dirLabel = "dimming"
		}
		d.infoLogger.Printf(
			"%s workspace %d: %d%% -> %d%% over %s (source=%s)",
			dirLabel, wsID, d.curPct, targetPct, transDur, source,
		)
	}

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-applyCh:
			applyCh = nil
			d.reloadConfigIfChanged()
			startTransition(pendingWS, pendingSource)

		case <-fadeCh:
			elapsed := time.Since(fadeStartTime)
			if elapsed >= fadeDuration {
				stopFade()
				applied, err := d.setter.Set(fadeTargetPct)
				if err != nil {
					d.warnLogger.Printf("failed to finalize brightness for workspace %d: %v", fadeWS, err)
					continue
				}
				d.curPct = applied
				d.curRaw = fadeTargetRaw
				ruleKind := "default"
				if fadeIsCustom {
					ruleKind = "configured"
				}
				d.infoLogger.Printf(
					"workspace %d -> %d%% (%s, smooth=%s, source=%s, device=%s)",
					fadeWS, applied, ruleKind, fadeDuration, fadeSource, d.setter.Device(),
				)
				continue
			}

			u := float64(elapsed) / float64(fadeDuration)
			var eased float64
			if fadeIsDimming {
				// Fast ease-out when dimming: sheds glare rapidly in the very first frames.
				inv := 1.0 - u
				eased = 1.0 - inv*inv
			} else {
				// Smoothstep when brightening: gentle start from dark so pupils adapt comfortably.
				eased = u * u * (3.0 - 2.0*u)
			}
			nextRaw := fadeStartRaw + int(math.Round(float64(fadeTargetRaw-fadeStartRaw)*eased))
			if nextRaw != lastSentRaw {
				if err := d.setter.SetRaw(nextRaw, d.maxRaw); err != nil {
					d.warnLogger.Printf("failed smooth brightness step for workspace %d: %v", fadeWS, err)
					stopFade()
					continue
				}
				lastSentRaw = nextRaw
				d.curRaw = nextRaw
				d.curPct = brightness.RawToPercent(nextRaw, d.maxRaw)
			}

		case line, ok := <-lineCh:
			if !ok {
				select {
				case err := <-errCh:
					if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
						return nil
					}
					return fmt.Errorf("error reading Hyprland event stream: %w", err)
				default:
					d.infoLogger.Println("Hyprland event stream closed; exiting")
					return nil
				}
			}

			wsID, evType, shouldHandle := d.resolveAndDebounce(line)
			if !shouldHandle {
				continue
			}

			d.reloadConfigIfChanged()

			// When not actively mid-fade, sync live hardware brightness in case user pressed Fn keys.
			if fadeCh == nil {
				d.syncHardwareState()
			}

			if applyCh != nil {
				d.infoLogger.Printf(
					"cancelled pending brightness change for workspace %d (switched to workspace %d)",
					pendingWS, wsID,
				)
			}
			cancelPending()

			targetPct, _ := d.cfg.BrightnessFor(wsID)
			delay := d.effectiveDelay()

			// Direction-aware delay:
			//  - Dimming (targetPct <= curPct): ALWAYS 0ms delay so screen dims immediately.
			//  - Explicit instant_workspaces: ALWAYS 0ms delay.
			//  - Brightening (targetPct > curPct): Wait delay_ms to avoid eye-searing flashes.
			isDimmingOrEqual := d.hasState && targetPct <= d.curPct
			isInstantWS := d.cfg.IsInstantWorkspace(wsID)

			if delay <= 0 || isDimmingOrEqual || isInstantWS {
				startTransition(wsID, evType)
				continue
			}

			// Stop any in-progress fade while waiting out the brighten delay.
			stopFade()

			pendingWS = wsID
			pendingSource = evType
			if applyTimer == nil {
				applyTimer = time.NewTimer(delay)
			} else {
				applyTimer.Reset(delay)
			}
			applyCh = applyTimer.C
			d.infoLogger.Printf(
				"scheduled brighten transition for workspace %d (%d%% -> %d%%) after %s delay",
				wsID, d.curPct, targetPct, delay,
			)
		}
	}
}

func (d *Daemon) resolveAndDebounce(line string) (int, string, bool) {
	ev, isWS, err := hyprland.ParseWorkspaceEvent(line)
	if err != nil {
		d.infoLogger.Printf("ignoring malformed workspace event: %v", err)
		return 0, "", false
	}
	if !isWS {
		return 0, "", false
	}

	wsID := ev.ID
	if !ev.HasID {
		if d.querier == nil {
			d.infoLogger.Printf("cannot resolve named workspace %q without querier", ev.Name)
			return 0, "", false
		}
		active, qErr := d.querier.ActiveWorkspace()
		if qErr != nil || active.ID <= 0 {
			d.infoLogger.Printf("failed to resolve ID for workspace %q: %v", ev.Name, qErr)
			return 0, "", false
		}
		wsID = active.ID
	}

	if !d.debouncer.ShouldProcess(wsID) {
		d.infoLogger.Printf("debounced duplicate event for workspace %d (%s)", wsID, ev.Type)
		return 0, "", false
	}

	return wsID, ev.Type, true
}

func (d *Daemon) applyImmediate(wsID int, source string) {
	targetPct, isCustom := d.cfg.BrightnessFor(wsID)
	applied, err := d.setter.Set(targetPct)
	if err != nil {
		d.warnLogger.Printf("failed to set brightness for workspace %d: %v", wsID, err)
		return
	}

	d.curPct = applied
	if d.maxRaw > 0 {
		d.curRaw = brightness.PercentToRaw(applied, d.maxRaw)
	}
	d.hasState = true

	ruleKind := "default"
	if isCustom {
		ruleKind = "configured"
	}
	d.infoLogger.Printf(
		"workspace %d -> %d%% (%s, source=%s, device=%s)",
		wsID, applied, ruleKind, source, d.setter.Device(),
	)
}
