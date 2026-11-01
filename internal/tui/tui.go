// Package tui implements the interactive Bubble Tea terminal interface for
// viewing and editing per-workspace brightness rules, brighten delay,
// per-workspace 0ms instant overrides, and smooth transition speed.
package tui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/adam0307a/hypr-ws-brightness/internal/brightness"
	"github.com/adam0307a/hypr-ws-brightness/internal/config"
	"github.com/adam0307a/hypr-ws-brightness/internal/hyprland"
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeEditBrightness
	modeEditDelay
	modeEditTransition
	modeEditDimTransition
	modeAddWorkspace
)

type rowKind int

const (
	rowKindDelay rowKind = iota
	rowKindTransition
	rowKindDimTransition
	rowKindDefault
	rowKindWorkspace
)

type rowItem struct {
	kind        rowKind
	workspaceID int
	name        string
	monitor     string
	windows     int
	isOpen      bool
}

// Options configures the TUI session.
type Options struct {
	ConfigPath     string
	DeviceOverride string
	Stderr         io.Writer
}

type styles struct {
	title      lipgloss.Style
	subtitle   lipgloss.Style
	box        lipgloss.Style
	cursor     lipgloss.Style
	selected   lipgloss.Style
	normal     lipgloss.Style
	activeTag  lipgloss.Style
	customTag  lipgloss.Style
	instantTag lipgloss.Style
	delayTag   lipgloss.Style
	fadeTag    lipgloss.Style
	inheritTag lipgloss.Style
	barFilled  lipgloss.Style
	barDelay   lipgloss.Style
	barFade    lipgloss.Style
	barEmpty   lipgloss.Style
	statusOK   lipgloss.Style
	statusWarn lipgloss.Style
	help       lipgloss.Style
	inputBox   lipgloss.Style
}

func defaultStyles() styles {
	return styles{
		title:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")),
		subtitle:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		box:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1),
		cursor:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")),
		selected:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")),
		normal:     lipgloss.NewStyle().Foreground(lipgloss.Color("250")),
		activeTag:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")),
		customTag:  lipgloss.NewStyle().Foreground(lipgloss.Color("81")),
		instantTag: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("226")),
		delayTag:   lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
		fadeTag:    lipgloss.NewStyle().Foreground(lipgloss.Color("45")),
		inheritTag: lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		barFilled:  lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
		barDelay:   lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
		barFade:    lipgloss.NewStyle().Foreground(lipgloss.Color("45")),
		barEmpty:   lipgloss.NewStyle().Foreground(lipgloss.Color("238")),
		statusOK:   lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		statusWarn: lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		help:       lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		inputBox:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")),
	}
}

type model struct {
	configPath   string
	cfg          *config.Config
	ctrl         *brightness.Controller
	hypr         *hyprland.Client
	inHyprland   bool
	activeWSID   int
	currentHW    int
	rows         []rowItem
	cursor       int
	mode         inputMode
	inputBuf     string
	applyOnAll   bool
	statusMsg    string
	statusIsWarn bool
	width        int
	height       int
	st           styles
}

// Run launches the interactive terminal UI.
func Run(opts Options) error {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.ConfigPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		opts.ConfigPath = p
	}

	loadRes := config.Load(opts.ConfigPath)
	var startupWarnings []string
	startupWarnings = append(startupWarnings, loadRes.Warnings...)

	inHypr := hyprland.InSession()
	if !inHypr {
		msg := "HYPRLAND_INSTANCE_SIGNATURE is not set; running outside Hyprland (showing configured workspaces)"
		fmt.Fprintf(opts.Stderr, "Warning: %s\n", msg)
		startupWarnings = append(startupWarnings, msg)
	}

	targetDevice := loadRes.Config.Device
	if opts.DeviceOverride != "" {
		targetDevice = opts.DeviceOverride
	}

	ctrl, err := brightness.NewController(targetDevice, func(w string) {
		startupWarnings = append(startupWarnings, w)
	})
	if err != nil {
		return err
	}

	currHW, hwErr := ctrl.Get()
	if hwErr != nil {
		startupWarnings = append(startupWarnings, hwErr.Error())
	}

	m := &model{
		configPath: opts.ConfigPath,
		cfg:        loadRes.Config,
		ctrl:       ctrl,
		hypr:       hyprland.NewClient(),
		inHyprland: inHypr,
		currentHW:  currHW,
		st:         defaultStyles(),
	}
	m.refreshWorkspaces()

	// Position cursor on the currently active workspace if found.
	for i, r := range m.rows {
		if r.kind == rowKindWorkspace && r.workspaceID == m.activeWSID {
			m.cursor = i
			break
		}
	}

	if len(startupWarnings) > 0 {
		m.setStatus(startupWarnings[0], true)
	} else {
		m.setStatus(fmt.Sprintf("Loaded %s (changes auto-save immediately)", opts.ConfigPath), false)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func (m *model) refreshWorkspaces() {
	openMap := make(map[int]hyprland.Workspace)
	idSet := make(map[int]bool)

	if m.inHyprland && m.hypr != nil {
		if active, err := m.hypr.ActiveWorkspace(); err == nil && active.ID > 0 {
			m.activeWSID = active.ID
			idSet[active.ID] = true
			openMap[active.ID] = active
		}
		if list, err := m.hypr.Workspaces(); err == nil {
			for _, ws := range list {
				idSet[ws.ID] = true
				openMap[ws.ID] = ws
			}
		}
	}

	for id := range m.cfg.Workspaces {
		if id > 0 {
			idSet[id] = true
		}
	}
	for id, enabled := range m.cfg.InstantWorkspaces {
		if enabled && id > 0 {
			idSet[id] = true
		}
	}

	// Preserve any extra unbound rows the user added during this TUI session.
	for _, existing := range m.rows {
		if existing.kind == rowKindWorkspace && existing.workspaceID > 0 {
			idSet[existing.workspaceID] = true
		}
	}

	// Seed workspaces 1..5 if no workspaces were discovered at all.
	if len(idSet) == 0 {
		for id := 1; id <= 5; id++ {
			idSet[id] = true
		}
	}

	ids := make([]int, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	rows := make([]rowItem, 0, len(ids)+4)
	rows = append(rows,
		rowItem{
			kind: rowKindDelay,
			name: "Brighten Delay",
		},
		rowItem{
			kind: rowKindTransition,
			name: "Brighten Speed",
		},
		rowItem{
			kind: rowKindDimTransition,
			name: "Dim Speed",
		},
		rowItem{
			kind: rowKindDefault,
			name: "Default (fallback)",
		},
	)
	for _, id := range ids {
		item := rowItem{
			kind:        rowKindWorkspace,
			workspaceID: id,
			name:        strconv.Itoa(id),
		}
		if ws, ok := openMap[id]; ok {
			item.isOpen = true
			if ws.Name != "" {
				item.name = ws.Name
			}
			item.monitor = ws.Monitor
			item.windows = ws.Windows
		}
		rows = append(rows, item)
	}

	m.rows = rows
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *model) refreshHardwareBrightness() {
	if pct, err := m.ctrl.Get(); err == nil {
		m.currentHW = pct
	}
}

func (m *model) setStatus(msg string, warn bool) {
	m.statusMsg = msg
	m.statusIsWarn = warn
}

func (m *model) rowBrightness(r rowItem) (pct int, custom bool) {
	if r.kind == rowKindDefault {
		return brightness.Clamp(m.cfg.DefaultBrightness), true
	}
	return m.cfg.BrightnessFor(r.workspaceID)
}

func (m *model) shouldAutoApply(r rowItem) bool {
	if r.kind == rowKindDelay || r.kind == rowKindTransition || r.kind == rowKindDimTransition {
		return false
	}
	if m.applyOnAll {
		return true
	}
	if m.activeWSID <= 0 {
		return false
	}
	if r.kind == rowKindDefault {
		_, hasCustom := m.cfg.Workspaces[m.activeWSID]
		return !hasCustom
	}
	return r.workspaceID == m.activeWSID
}

func (m *model) applyDelayValue(newMs int) {
	clamped := config.ClampDelay(newMs)
	m.cfg.DelayMs = clamped
	if err := m.cfg.Save(m.configPath); err != nil {
		m.setStatus(fmt.Sprintf("Failed to save config: %v", err), true)
		return
	}
	if clamped == 0 {
		m.setStatus("Brighten delay -> 0 ms (all transitions begin immediately, saved)", false)
	} else {
		m.setStatus(fmt.Sprintf("Brighten delay -> %d ms (%.2fs on low->high; high->low is always 0ms, saved)", clamped, float64(clamped)/1000.0), false)
	}
}

func (m *model) applyTransitionValue(newMs int) {
	clamped := config.ClampTransition(newMs)
	m.cfg.TransitionMs = clamped
	if err := m.cfg.Save(m.configPath); err != nil {
		m.setStatus(fmt.Sprintf("Failed to save config: %v", err), true)
		return
	}
	if clamped == 0 {
		m.setStatus("Brighten speed -> 0 ms (instant step on low->high, saved)", false)
	} else {
		m.setStatus(fmt.Sprintf("Brighten speed -> %d ms (%.2fs slow fade on low->high, saved)", clamped, float64(clamped)/1000.0), false)
	}
}

func (m *model) applyDimTransitionValue(newMs int) {
	clamped := config.ClampTransition(newMs)
	m.cfg.DimTransitionMs = clamped
	if err := m.cfg.Save(m.configPath); err != nil {
		m.setStatus(fmt.Sprintf("Failed to save config: %v", err), true)
		return
	}
	if clamped == 0 {
		m.setStatus("Dim speed -> 0 ms (instant drop on high->low, saved)", false)
	} else {
		m.setStatus(fmt.Sprintf("Dim speed -> %d ms (%.2fs fast dim on high->low, saved)", clamped, float64(clamped)/1000.0), false)
	}
}

func (m *model) toggleSelectedInstant() {
	if len(m.rows) == 0 {
		return
	}
	r := m.rows[m.cursor]
	if r.kind != rowKindWorkspace {
		m.setStatus("Select a Workspace row to toggle 0ms instant override (dimming is always 0ms)", true)
		return
	}

	nowInstant := m.cfg.ToggleInstantWorkspace(r.workspaceID)
	if err := m.cfg.Save(m.configPath); err != nil {
		m.setStatus(fmt.Sprintf("Failed to save config: %v", err), true)
		return
	}

	if nowInstant {
		m.setStatus(fmt.Sprintf("Workspace %d marked ⚡ 0ms delay (always skips brighten delay)", r.workspaceID), false)
	} else {
		m.setStatus(fmt.Sprintf("Workspace %d uses normal brighten delay (%d ms on low->high, 0ms on high->low)", r.workspaceID, m.cfg.DelayMs), false)
	}
}

func (m *model) applyRowValue(r rowItem, newVal int, forceApplyHW bool) {
	switch r.kind {
	case rowKindDelay:
		m.applyDelayValue(newVal)
		return
	case rowKindTransition:
		m.applyTransitionValue(newVal)
		return
	case rowKindDimTransition:
		m.applyDimTransitionValue(newVal)
		return
	}

	clamped := brightness.Clamp(newVal)
	var label string
	if r.kind == rowKindDefault {
		m.cfg.DefaultBrightness = clamped
		label = "Default brightness"
	} else {
		m.cfg.SetWorkspace(r.workspaceID, clamped)
		label = fmt.Sprintf("Workspace %d", r.workspaceID)
	}

	if err := m.cfg.Save(m.configPath); err != nil {
		m.setStatus(fmt.Sprintf("Failed to save config: %v", err), true)
		return
	}

	if forceApplyHW || m.shouldAutoApply(r) {
		applied, err := m.ctrl.Set(clamped)
		if err != nil {
			m.setStatus(fmt.Sprintf("Saved %s = %d%%, but hardware apply failed: %v", label, clamped, err), true)
			return
		}
		m.currentHW = applied
		m.setStatus(fmt.Sprintf("%s -> %d%% (saved & applied to %s)", label, applied, m.ctrl.Device()), false)
		return
	}

	m.setStatus(fmt.Sprintf("%s -> %d%% (saved)", label, clamped), false)
}

func (m *model) resetSelectedRow() {
	if len(m.rows) == 0 {
		return
	}
	r := m.rows[m.cursor]
	switch r.kind {
	case rowKindDelay:
		m.applyDelayValue(config.DefaultDelayMs)
		return
	case rowKindTransition:
		m.applyTransitionValue(config.DefaultTransitionMs)
		return
	case rowKindDimTransition:
		m.applyDimTransitionValue(config.DefaultDimTransitionMs)
		return
	case rowKindDefault:
		m.applyRowValue(r, config.DefaultBrightnessValue, false)
		return
	}

	_, hasCustom := m.cfg.Workspaces[r.workspaceID]
	hasInstant := m.cfg.IsInstantWorkspace(r.workspaceID)
	if !hasCustom && !hasInstant {
		m.setStatus(fmt.Sprintf("Workspace %d already uses default settings (%d%%)", r.workspaceID, m.cfg.DefaultBrightness), false)
		return
	}

	m.cfg.DeleteWorkspace(r.workspaceID)
	if err := m.cfg.Save(m.configPath); err != nil {
		m.setStatus(fmt.Sprintf("Failed to save config: %v", err), true)
		return
	}

	if m.shouldAutoApply(r) || r.workspaceID == m.activeWSID {
		if applied, err := m.ctrl.Set(m.cfg.DefaultBrightness); err == nil {
			m.currentHW = applied
		}
	}
	m.setStatus(fmt.Sprintf("Reset Workspace %d to default brightness (%d%%) and normal delay", r.workspaceID, m.cfg.DefaultBrightness), false)
}

func (m *model) captureCurrentToSelected() {
	if len(m.rows) == 0 {
		return
	}
	r := m.rows[m.cursor]
	if r.kind == rowKindDelay || r.kind == rowKindTransition || r.kind == rowKindDimTransition {
		m.setStatus("Select a Workspace or Default row to capture current hardware brightness", true)
		return
	}

	curr, err := m.ctrl.Get()
	if err != nil {
		m.setStatus(fmt.Sprintf("Failed to read current hardware brightness: %v", err), true)
		return
	}
	m.currentHW = curr
	m.applyRowValue(r, curr, false)
	if r.kind == rowKindDefault {
		m.setStatus(fmt.Sprintf("Captured current brightness (%d%%) -> Default brightness", curr), false)
	} else {
		m.setStatus(fmt.Sprintf("Captured current brightness (%d%%) -> Workspace %d", curr, r.workspaceID), false)
	}
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case modeEditBrightness:
			return m.updateEditNumber(msg, 3, func(v int) {
				m.applyRowValue(m.rows[m.cursor], v, false)
			})
		case modeEditDelay:
			return m.updateEditNumber(msg, 5, m.applyDelayValue)
		case modeEditTransition:
			return m.updateEditNumber(msg, 5, m.applyTransitionValue)
		case modeEditDimTransition:
			return m.updateEditNumber(msg, 5, m.applyDimTransitionValue)
		case modeAddWorkspace:
			return m.updateAddWorkspace(msg)
		default:
			return m.updateNormal(msg)
		}
	}
	return m, nil
}

func (m *model) updateEditNumber(msg tea.KeyMsg, maxDigits int, applyFn func(int)) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeNormal
		m.inputBuf = ""
		m.setStatus("Cancelled numeric input", false)
		return m, nil
	case "enter":
		m.mode = modeNormal
		raw := strings.TrimSpace(m.inputBuf)
		m.inputBuf = ""
		if raw == "" {
			return m, nil
		}
		val, err := strconv.Atoi(raw)
		if err != nil {
			m.setStatus(fmt.Sprintf("Invalid number %q", raw), true)
			return m, nil
		}
		applyFn(val)
		return m, nil
	case "backspace":
		if len(m.inputBuf) > 0 {
			m.inputBuf = m.inputBuf[:len(m.inputBuf)-1]
		}
		return m, nil
	default:
		if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && len(m.inputBuf) < maxDigits {
			m.inputBuf += key
		}
		return m, nil
	}
}

func (m *model) updateAddWorkspace(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeNormal
		m.inputBuf = ""
		m.setStatus("Cancelled adding workspace", false)
		return m, nil
	case "enter":
		m.mode = modeNormal
		raw := strings.TrimSpace(m.inputBuf)
		m.inputBuf = ""
		if raw == "" {
			return m, nil
		}
		wsID, err := strconv.Atoi(raw)
		if err != nil || wsID <= 0 {
			m.setStatus("Workspace ID must be a positive integer", true)
			return m, nil
		}
		if _, exists := m.cfg.Workspaces[wsID]; !exists {
			m.cfg.SetWorkspace(wsID, m.cfg.DefaultBrightness)
			_ = m.cfg.Save(m.configPath)
		}
		m.refreshWorkspaces()
		for i, r := range m.rows {
			if r.kind == rowKindWorkspace && r.workspaceID == wsID {
				m.cursor = i
				break
			}
		}
		m.setStatus(fmt.Sprintf("Added Workspace %d (%d%%)", wsID, m.cfg.Workspaces[wsID]), false)
		return m, nil
	case "backspace":
		if len(m.inputBuf) > 0 {
			m.inputBuf = m.inputBuf[:len(m.inputBuf)-1]
		}
		return m, nil
	default:
		if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && len(m.inputBuf) < 4 {
			m.inputBuf += key
		}
		return m, nil
	}
}

func (m *model) beginEditForRow(initial string) {
	if len(m.rows) == 0 {
		return
	}
	switch m.rows[m.cursor].kind {
	case rowKindDelay:
		m.mode = modeEditDelay
	case rowKindTransition:
		m.mode = modeEditTransition
	case rowKindDimTransition:
		m.mode = modeEditDimTransition
	default:
		m.mode = modeEditBrightness
	}
	m.inputBuf = initial
}

func (m *model) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}

	case "left", "h":
		if len(m.rows) > 0 {
			r := m.rows[m.cursor]
			switch r.kind {
			case rowKindDelay:
				m.applyDelayValue(m.cfg.DelayMs - 100)
			case rowKindTransition:
				m.applyTransitionValue(m.cfg.TransitionMs - 100)
			case rowKindDimTransition:
				m.applyDimTransitionValue(m.cfg.DimTransitionMs - 50)
			default:
				pct, _ := m.rowBrightness(r)
				m.applyRowValue(r, pct-5, false)
			}
		}
	case "right", "l":
		if len(m.rows) > 0 {
			r := m.rows[m.cursor]
			switch r.kind {
			case rowKindDelay:
				m.applyDelayValue(m.cfg.DelayMs + 100)
			case rowKindTransition:
				m.applyTransitionValue(m.cfg.TransitionMs + 100)
			case rowKindDimTransition:
				m.applyDimTransitionValue(m.cfg.DimTransitionMs + 50)
			default:
				pct, _ := m.rowBrightness(r)
				m.applyRowValue(r, pct+5, false)
			}
		}

	case "-", "[":
		if len(m.rows) > 0 {
			r := m.rows[m.cursor]
			switch r.kind {
			case rowKindDelay:
				m.applyDelayValue(m.cfg.DelayMs - 50)
			case rowKindTransition:
				m.applyTransitionValue(m.cfg.TransitionMs - 50)
			case rowKindDimTransition:
				m.applyDimTransitionValue(m.cfg.DimTransitionMs - 10)
			default:
				pct, _ := m.rowBrightness(r)
				m.applyRowValue(r, pct-1, false)
			}
		}
	case "+", "=", "]":
		if len(m.rows) > 0 {
			r := m.rows[m.cursor]
			switch r.kind {
			case rowKindDelay:
				m.applyDelayValue(m.cfg.DelayMs + 50)
			case rowKindTransition:
				m.applyTransitionValue(m.cfg.TransitionMs + 50)
			case rowKindDimTransition:
				m.applyDimTransitionValue(m.cfg.DimTransitionMs + 10)
			default:
				pct, _ := m.rowBrightness(r)
				m.applyRowValue(r, pct+1, false)
			}
		}

	case "enter", "e":
		m.beginEditForRow("")

	case "i":
		m.toggleSelectedInstant()

	case "y":
		for i, r := range m.rows {
			if r.kind == rowKindDelay {
				m.cursor = i
				break
			}
		}
		m.mode = modeEditDelay
		m.inputBuf = ""

	case "f":
		for i, r := range m.rows {
			if r.kind == rowKindTransition {
				m.cursor = i
				break
			}
		}
		m.mode = modeEditTransition
		m.inputBuf = ""

	case "c":
		m.captureCurrentToSelected()

	case "p", " ":
		if len(m.rows) > 0 {
			r := m.rows[m.cursor]
			if r.kind == rowKindDelay || r.kind == rowKindTransition || r.kind == rowKindDimTransition {
				m.setStatus("Select a Workspace or Default row to preview brightness on hardware", true)
				break
			}
			pct, _ := m.rowBrightness(r)
			applied, err := m.ctrl.Set(pct)
			if err != nil {
				m.setStatus(fmt.Sprintf("Failed to apply preview: %v", err), true)
			} else {
				m.currentHW = applied
				m.setStatus(fmt.Sprintf("Applied %d%% immediately to %s", applied, m.ctrl.Device()), false)
			}
		}

	case "t":
		m.applyOnAll = !m.applyOnAll
		if m.applyOnAll {
			m.setStatus("Live hardware preview enabled for all rows", false)
		} else {
			m.setStatus("Live hardware apply restricted to active workspace only", false)
		}

	case "s":
		if err := m.cfg.Save(m.configPath); err != nil {
			m.setStatus(fmt.Sprintf("Failed to save config: %v", err), true)
		} else {
			m.setStatus(fmt.Sprintf("Saved configuration to %s", m.configPath), false)
		}

	case "a":
		m.mode = modeAddWorkspace
		m.inputBuf = ""

	case "d", "x", "delete", "backspace":
		m.resetSelectedRow()

	case "r":
		m.refreshWorkspaces()
		m.refreshHardwareBrightness()
		m.setStatus("Refreshed workspaces and hardware brightness", false)

	default:
		if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && len(m.rows) > 0 {
			m.beginEditForRow(key)
		}
	}

	return m, nil
}

func renderBar(pct int, st styles) string {
	const totalCells = 20
	filled := (brightness.Clamp(pct)*totalCells + 50) / 100
	if filled > totalCells {
		filled = totalCells
	}
	empty := totalCells - filled
	return st.barFilled.Render(strings.Repeat("█", filled)) +
		st.barEmpty.Render(strings.Repeat("░", empty))
}

func renderDurationBar(ms, visualCapMs int, fillStyle, emptyStyle lipgloss.Style) string {
	const totalCells = 20
	clamped := ms
	if clamped < 0 {
		clamped = 0
	}
	if clamped > visualCapMs {
		clamped = visualCapMs
	}
	filled := (clamped*totalCells + visualCapMs/2) / visualCapMs
	if ms > 0 && filled == 0 {
		filled = 1
	}
	if filled > totalCells {
		filled = totalCells
	}
	empty := totalCells - filled
	return fillStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", empty))
}

func (m *model) View() string {
	var b strings.Builder

	header := m.st.title.Render("hypr-ws-brightness") + " " +
		m.st.subtitle.Render("— Hyprland Per-Workspace Hardware Brightness")
	b.WriteString(header + "\n")

	metaLine := fmt.Sprintf(
		"Device: %s   Current: %d%%   Delay: %dms   Brighten: %dms   Dim: %dms",
		m.ctrl.Device(), m.currentHW, m.cfg.DelayMs, m.cfg.TransitionMs, m.cfg.DimTransitionMs,
	)
	b.WriteString(m.st.subtitle.Render(metaLine) + "\n\n")

	var listBuilder strings.Builder
	for i, r := range m.rows {
		selected := i == m.cursor
		prefix := "  "
		if selected {
			prefix = m.st.cursor.Render("▸ ")
		}

		if r.kind == rowKindDelay {
			label := fmt.Sprintf("%-22s", r.name)
			if selected {
				label = m.st.selected.Render(label)
			} else {
				label = m.st.normal.Render(label)
			}

			bar := renderDurationBar(m.cfg.DelayMs, 3000, m.st.barDelay, m.st.barEmpty)
			valDisplay := fmt.Sprintf("%4dms", m.cfg.DelayMs)
			if selected && m.mode == modeEditDelay {
				valDisplay = m.st.inputBox.Render(fmt.Sprintf("[%s_ms]", m.inputBuf))
			} else if selected {
				valDisplay = m.st.selected.Render(valDisplay)
			}

			var badge string
			if m.cfg.DelayMs == 0 {
				badge = m.st.subtitle.Render("instant (0ms)")
			} else {
				badge = m.st.delayTag.Render(fmt.Sprintf("%.2fs on low→high · high→low=0ms", float64(m.cfg.DelayMs)/1000.0))
			}

			fmt.Fprintf(&listBuilder, "%s%s  %s  %s   %s\n", prefix, label, bar, valDisplay, badge)
			continue
		}

		if r.kind == rowKindTransition {
			label := fmt.Sprintf("%-22s", r.name)
			if selected {
				label = m.st.selected.Render(label)
			} else {
				label = m.st.normal.Render(label)
			}

			bar := renderDurationBar(m.cfg.TransitionMs, 2500, m.st.barFade, m.st.barEmpty)
			valDisplay := fmt.Sprintf("%4dms", m.cfg.TransitionMs)
			if selected && m.mode == modeEditTransition {
				valDisplay = m.st.inputBox.Render(fmt.Sprintf("[%s_ms]", m.inputBuf))
			} else if selected {
				valDisplay = m.st.selected.Render(valDisplay)
			}

			var badge string
			if m.cfg.TransitionMs == 0 {
				badge = m.st.subtitle.Render("instant step (low→high)")
			} else {
				badge = m.st.fadeTag.Render(fmt.Sprintf("%.2fs slow rise (low→high)", float64(m.cfg.TransitionMs)/1000.0))
			}

			fmt.Fprintf(&listBuilder, "%s%s  %s  %s   %s\n", prefix, label, bar, valDisplay, badge)
			continue
		}

		if r.kind == rowKindDimTransition {
			label := fmt.Sprintf("%-22s", r.name)
			if selected {
				label = m.st.selected.Render(label)
			} else {
				label = m.st.normal.Render(label)
			}

			bar := renderDurationBar(m.cfg.DimTransitionMs, 1000, m.st.barFade, m.st.barEmpty)
			valDisplay := fmt.Sprintf("%4dms", m.cfg.DimTransitionMs)
			if selected && m.mode == modeEditDimTransition {
				valDisplay = m.st.inputBox.Render(fmt.Sprintf("[%s_ms]", m.inputBuf))
			} else if selected {
				valDisplay = m.st.selected.Render(valDisplay)
			}

			var badge string
			if m.cfg.DimTransitionMs == 0 {
				badge = m.st.instantTag.Render("⚡ instant drop (high→low)")
			} else {
				badge = m.st.fadeTag.Render(fmt.Sprintf("%.2fs fast dim (high→low)", float64(m.cfg.DimTransitionMs)/1000.0))
			}

			fmt.Fprintf(&listBuilder, "%s%s  %s  %s   %s\n", prefix, label, bar, valDisplay, badge)
			continue
		}

		pct, isCustom := m.rowBrightness(r)
		bar := renderBar(pct, m.st)

		var label string
		if r.kind == rowKindDefault {
			label = fmt.Sprintf("%-22s", r.name)
		} else {
			wsTitle := fmt.Sprintf("Workspace %-2d", r.workspaceID)
			if r.name != "" && r.name != strconv.Itoa(r.workspaceID) {
				wsTitle = fmt.Sprintf("Workspace %-2d (%s)", r.workspaceID, r.name)
			}
			label = fmt.Sprintf("%-22s", wsTitle)
		}

		if selected {
			label = m.st.selected.Render(label)
		} else {
			label = m.st.normal.Render(label)
		}

		pctDisplay := fmt.Sprintf("  %3d%%", pct)
		if selected && m.mode == modeEditBrightness {
			pctDisplay = m.st.inputBox.Render(fmt.Sprintf("[%s_%%]", m.inputBuf))
		} else if selected {
			pctDisplay = m.st.selected.Render(pctDisplay)
		}

		var badges []string
		if r.kind == rowKindWorkspace {
			if isCustom {
				badges = append(badges, m.st.customTag.Render("custom"))
			} else {
				badges = append(badges, m.st.inheritTag.Render("default"))
			}
			if m.cfg.IsInstantWorkspace(r.workspaceID) {
				badges = append(badges, m.st.instantTag.Render("⚡ 0ms delay"))
			}
			if r.workspaceID == m.activeWSID && m.activeWSID > 0 {
				badges = append(badges, m.st.activeTag.Render("● active"))
			}
			if r.isOpen && r.monitor != "" {
				badges = append(badges, m.st.subtitle.Render(fmt.Sprintf("%s · %dw", r.monitor, r.windows)))
			}
		} else {
			badges = append(badges, m.st.subtitle.Render("fallback"))
		}

		line := fmt.Sprintf("%s%s  %s  %s", prefix, label, bar, pctDisplay)
		if len(badges) > 0 {
			line += "   " + strings.Join(badges, "  ")
		}
		listBuilder.WriteString(line)
		if i < len(m.rows)-1 {
			listBuilder.WriteString("\n")
		}
	}

	b.WriteString(m.st.box.Render(listBuilder.String()) + "\n\n")

	switch m.mode {
	case modeEditBrightness:
		prompt := fmt.Sprintf("Enter brightness percentage (0-100): %s_   [Enter: confirm, Esc: cancel]", m.inputBuf)
		b.WriteString(m.st.inputBox.Render(prompt) + "\n")
	case modeEditDelay:
		prompt := fmt.Sprintf("Enter brighten delay in ms (0-30000, low->high wait; high->low is always 0ms): %s_   [Enter: confirm, Esc: cancel]", m.inputBuf)
		b.WriteString(m.st.inputBox.Render(prompt) + "\n")
	case modeEditTransition:
		prompt := fmt.Sprintf("Enter brighten (low->high) fade duration in ms (0-10000, e.g. 800 = 0.8s): %s_   [Enter: confirm, Esc: cancel]", m.inputBuf)
		b.WriteString(m.st.inputBox.Render(prompt) + "\n")
	case modeEditDimTransition:
		prompt := fmt.Sprintf("Enter dim (high->low) duration in ms (0 = instant drop, e.g. 100 = 0.1s): %s_   [Enter: confirm, Esc: cancel]", m.inputBuf)
		b.WriteString(m.st.inputBox.Render(prompt) + "\n")
	case modeAddWorkspace:
		prompt := fmt.Sprintf("Add workspace ID (positive integer): %s_   [Enter: add, Esc: cancel]", m.inputBuf)
		b.WriteString(m.st.inputBox.Render(prompt) + "\n")
	default:
		if m.statusMsg != "" {
			if m.statusIsWarn {
				b.WriteString(m.st.statusWarn.Render("⚠ "+m.statusMsg) + "\n")
			} else {
				b.WriteString(m.st.statusOK.Render("✓ "+m.statusMsg) + "\n")
			}
		} else {
			b.WriteString("\n")
		}
	}

	helpLines := []string{
		"↑/↓ j/k: select   ←/→ h/l: ±5% (±100ms)   -/+: ±1% (±50ms)   0-9/Enter: type value   i: toggle ⚡ 0ms delay on ws",
		"c: capture brightness   y: edit delay   f: edit fade speed   p/Space: apply now   a: add ws   d: reset   q: quit",
	}
	b.WriteString("\n" + m.st.help.Render(strings.Join(helpLines, "\n")) + "\n")

	return b.String()
}
