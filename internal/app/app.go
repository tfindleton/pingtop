package app

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tfindleton/pingtop/internal/checks"
	"github.com/tfindleton/pingtop/internal/pingtop"
	termui "github.com/tfindleton/pingtop/internal/ui"
	"github.com/tfindleton/pingtop/internal/updates"
)

type AppConfig = pingtop.AppConfig
type CSVLogger = pingtop.CSVLogger
type CheckCoordinator = checks.CheckCoordinator
type CheckResult = pingtop.CheckResult
type ConfigManager = pingtop.ConfigManager
type PromptState = pingtop.PromptState
type Renderer = termui.Renderer
type RuntimePaths = pingtop.RuntimePaths
type StateSnapshot = pingtop.StateSnapshot
type StateStore = pingtop.StateStore
type TargetSpec = pingtop.TargetSpec
type UpdateManager = updates.UpdateManager
type UpdateStatus = updates.UpdateStatus

var checkUpdatesNow = func(currentVersion, repoURL string, enabled bool) UpdateStatus {
	manager := updates.NewUpdateManager(currentVersion, repoURL, enabled, nil)
	return manager.CheckNow()
}

type cliArgs struct {
	noUI         bool
	once         bool
	showHelp     bool
	showVersion  bool
	checkUpdates bool
	updateRepo   string
	forceVersion string
	targets      []string
}

type AppServices struct {
	runtimePaths  RuntimePaths
	configManager *ConfigManager
	stateStore    *StateStore
	logger        *CSVLogger
	coordinator   *CheckCoordinator
	updateManager *UpdateManager
}

func buildExitSummary(snapshot StateSnapshot) string {
	return fmt.Sprintf(
		"Session summary: cycles=%d, checks=%d, ok=%d, failures=%d, dns_failures=%d, ping_failures=%d, diagnosis=%s",
		snapshot.Session.CyclesCompleted,
		snapshot.Session.TotalChecks,
		snapshot.Session.Successes,
		snapshot.Session.Failures,
		snapshot.Session.DNSFailures,
		snapshot.Session.PingFailures,
		snapshot.Diagnosis,
	)
}

func printInteractiveExitSummary(snapshot StateSnapshot) {
	renderer := termui.NewRenderer()
	fmt.Println()
	fmt.Println(renderer.BuildExitSummary(snapshot))
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: pingtop [flags] [target ...]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Flags:")
	fmt.Fprintln(output, "  -h, --help     show help and exit")
	fmt.Fprintln(output, "  -n, --no-ui    run in headless text mode")
	fmt.Fprintln(output, "  -o, --once     run a single cycle and exit")
	fmt.Fprintln(output, "  -u, --updates  run one update check and exit")
	fmt.Fprintln(output, "  -v, --version  print version and exit")
	fmt.Fprintln(output, "      --check-updates         same as --updates")
	fmt.Fprintln(output, "      --update-repo URL       override the update repository URL for this run")
	fmt.Fprintln(output, "      --current-version VAL   override the current version for update testing")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Positional targets:")
	fmt.Fprintln(output, "  One or more hostnames or IPs to monitor for this run only.")
	fmt.Fprintln(output, "  When targets are passed on the command line, CSV logging is disabled.")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Examples:")
	fmt.Fprintln(output, "  pingtop")
	fmt.Fprintln(output, "  pingtop -v")
	fmt.Fprintln(output, "  pingtop -o")
	fmt.Fprintln(output, "  pingtop -n")
	fmt.Fprintln(output, "  pingtop -u")
	fmt.Fprintln(output, "  pingtop --updates")
	fmt.Fprintln(output, "  pingtop 1.1.1.1")
	fmt.Fprintln(output, "  pingtop example.com 1.1.1.1")
	fmt.Fprintln(output, "  pingtop -n example.com 1.1.1.1")
	fmt.Fprintln(output, "  pingtop --updates")
	fmt.Fprintln(output, "  pingtop --check-updates --current-version 0.1.3")
}

func printCycleSummary(results []CheckResult, snapshot StateSnapshot) {
	timestamp := pingtop.NowLocalISO(time.Time{}, false)
	if len(results) == 0 {
		fmt.Printf("[%s] no targets configured\n", timestamp)
		return
	}
	failures := 0
	for _, result := range results {
		if result.IsFailure() {
			failures++
		}
	}
	fmt.Printf(
		"[%s] cycle %d: diagnosis=%s; ok=%d fail=%d\n",
		timestamp,
		results[0].CycleID,
		snapshot.Diagnosis,
		len(results)-failures,
		failures,
	)
	for _, result := range results {
		status := "OK"
		if result.IsFailure() {
			status = strings.ToUpper(result.StatusText())
		}
		fmt.Printf(
			"  - %-18s %-9s ip=%-15s lat=%8s err=%s\n",
			result.Target,
			status,
			pingtop.DefaultString(result.ResolvedIP, "-"),
			pingtop.FormatLatency(result.LatencyMS),
			pingtop.HumanErrorMessage(result),
		)
	}
}

func runHeadless(
	configManager *ConfigManager,
	stateStore *StateStore,
	logger *CSVLogger,
	coordinator *CheckCoordinator,
	once bool,
) int {
	if warning := configManager.LoadWarning(); warning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}

	monitor := NewBackgroundMonitor(configManager, stateStore, logger, coordinator)
	if once {
		config := configManager.Snapshot()
		results := monitor.RunSingleCycle(config)
		stateStore.HandleCycle(results, config, monitor.CurrentCycleID())
		logger.LogResults(results, config)
		printCycleSummary(results, stateStore.Snapshot())
		fmt.Println(buildExitSummary(stateStore.Snapshot()))
		return 0
	}

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt)
	defer signal.Stop(signalCh)

	fmt.Println("pingtop headless mode. Press Ctrl+C to stop.")
	for {
		select {
		case <-signalCh:
			fmt.Println(buildExitSummary(stateStore.Snapshot()))
			return 0
		default:
		}

		config := configManager.Snapshot()
		results := monitor.RunSingleCycle(config)
		stateStore.HandleCycle(results, config, monitor.CurrentCycleID())
		logger.LogResults(results, config)
		printCycleSummary(results, stateStore.Snapshot())

		timer := time.NewTimer(time.Duration(config.CheckIntervalSeconds * float64(time.Second)))
		select {
		case <-signalCh:
			timer.Stop()
			fmt.Println(buildExitSummary(stateStore.Snapshot()))
			return 0
		case <-timer.C:
		}
	}
}

func parseArgs(argv []string) (cliArgs, error) {
	flags := flag.NewFlagSet("pingtop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	args := cliArgs{}
	flags.BoolVar(&args.showHelp, "h", false, "show help and exit")
	flags.BoolVar(&args.showHelp, "help", false, "show help and exit")
	flags.BoolVar(&args.noUI, "n", false, "run in headless text mode")
	flags.BoolVar(&args.noUI, "no-ui", false, "run in headless text mode")
	flags.BoolVar(&args.once, "o", false, "run a single cycle and exit")
	flags.BoolVar(&args.once, "once", false, "run a single cycle and exit")
	flags.BoolVar(&args.checkUpdates, "u", false, "run one update check and exit")
	flags.BoolVar(&args.checkUpdates, "updates", false, "run one update check and exit")
	flags.BoolVar(&args.showVersion, "v", false, "print version and exit")
	flags.BoolVar(&args.showVersion, "version", false, "print version and exit")
	flags.BoolVar(&args.checkUpdates, "check-updates", false, "run one update check and exit")
	flags.StringVar(&args.updateRepo, "update-repo", "", "override the update repository URL for this run")
	flags.StringVar(&args.forceVersion, "current-version", "", "override the current version for update testing")
	if err := flags.Parse(argv); err != nil {
		return args, err
	}
	args.targets = append([]string(nil), flags.Args()...)
	return args, nil
}

func parseTargetArgs(values []string) ([]TargetSpec, error) {
	targets := make([]TargetSpec, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		target, err := pingtop.InferTarget(value)
		if err != nil {
			return nil, fmt.Errorf("invalid target %q: %w", value, err)
		}
		key := target.Kind + ":" + target.Value
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return targets, nil
}

func buildServices(runtimePaths RuntimePaths, args cliArgs) (AppServices, error) {
	configManager := pingtop.NewConfigManager(runtimePaths.ConfigPath)
	config := configManager.Snapshot()
	logger := pingtop.NewDisabledCSVLogger()
	if len(args.targets) > 0 {
		targets, err := parseTargetArgs(args.targets)
		if err != nil {
			return AppServices{}, err
		}
		config.Targets = targets
		configManager = pingtop.NewTransientConfigManager(config)
	} else {
		logger = pingtop.NewCSVLogger(runtimePaths.LogPath)
	}
	stateStore := pingtop.NewStateStore(configManager.Snapshot())
	if len(args.targets) > 0 {
		stateStore.AddEvent("info", fmt.Sprintf("Using command-line targets (%d); CSV logging disabled", len(config.Targets)), time.Time{})
	}
	coordinator := checks.NewCheckCoordinator(checks.NewPingRunner(), nil)
	config = configManager.Snapshot()
	updateManager := updates.NewUpdateManager("v"+pingtop.Version, config.UpdateRepoURL, config.UpdateCheckEnabled, nil)
	return AppServices{
		runtimePaths:  runtimePaths,
		configManager: configManager,
		stateStore:    stateStore,
		logger:        logger,
		coordinator:   coordinator,
		updateManager: updateManager,
	}, nil
}

func Run(argv []string) int {
	args, err := parseArgs(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		writeUsage(os.Stderr)
		return 2
	}
	if args.showHelp {
		writeUsage(os.Stdout)
		return 0
	}
	if args.showVersion {
		fmt.Println(pingtop.Version)
		return 0
	}
	if args.checkUpdates {
		return runUpdateCheck(os.Stdout, os.Stderr, args)
	}

	services, err := buildServices(pingtop.ResolveRuntimePaths(), args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		writeUsage(os.Stderr)
		return 2
	}
	defer services.coordinator.Close()

	if args.noUI || args.once {
		return runHeadless(
			services.configManager,
			services.stateStore,
			services.logger,
			services.coordinator,
			args.once,
		)
	}

	if !termui.SupportsTTY(os.Stdin) || !termui.SupportsTTY(os.Stdout) || !termui.InteractiveSupported() {
		fmt.Println("Interactive UI requires a supported TTY; falling back to --no-ui mode.")
		return runHeadless(
			services.configManager,
			services.stateStore,
			services.logger,
			services.coordinator,
			false,
		)
	}

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)
	result := ui.Run()
	printInteractiveExitSummary(services.stateStore.Snapshot())
	return result
}

func runUpdateCheck(output io.Writer, errorOutput io.Writer, args cliArgs) int {
	configManager := pingtop.NewConfigManager(pingtop.ResolveRuntimePaths().ConfigPath)
	if warning := configManager.LoadWarning(); warning != "" {
		fmt.Fprintf(errorOutput, "warning: %s\n", warning)
	}

	config := configManager.Snapshot()
	repoURL := strings.TrimSpace(config.UpdateRepoURL)
	if override := strings.TrimSpace(args.updateRepo); override != "" {
		repoURL = override
	}
	if repoURL == "" {
		fmt.Fprintln(errorOutput, "error: no update repository URL configured")
		return 2
	}

	currentVersion := "v" + pingtop.Version
	if override := strings.TrimSpace(args.forceVersion); override != "" {
		currentVersion = override
	}

	status := checkUpdatesNow(currentVersion, repoURL, true)
	fmt.Fprintln(output, "Update check")
	fmt.Fprintf(output, "  current version: %s\n", currentVersion)
	fmt.Fprintf(output, "  repo URL: %s\n", repoURL)
	fmt.Fprintf(output, "  state: %s\n", status.State)
	if status.LatestVersion != "" {
		fmt.Fprintf(output, "  latest version: %s\n", status.LatestVersion)
	}
	if status.ReleaseURL != "" {
		fmt.Fprintf(output, "  release URL: %s\n", status.ReleaseURL)
	}
	if status.ErrorMessage != "" {
		fmt.Fprintf(output, "  error: %s\n", status.ErrorMessage)
		return 1
	}
	return 0
}

type PingTopUI struct {
	runtimePaths      RuntimePaths
	configManager     *ConfigManager
	stateStore        *StateStore
	logger            *CSVLogger
	coordinator       *CheckCoordinator
	updateManager     *UpdateManager
	monitor           *BackgroundMonitor
	renderer          *Renderer
	helpVisible       bool
	prompt            *PromptState
	eventScrollOffset int
	running           bool
	lastUpdateState   string
	dirty             bool
	lastRender        uiRenderState
	hasLastRender     bool
}

type uiRenderState struct {
	StateRevision     uint64
	ConfigRevision    uint64
	UpdateStatus      UpdateStatus
	Paused            bool
	HelpVisible       bool
	EventScrollOffset int
	HasPrompt         bool
	Prompt            PromptState
	Width             int
	Height            int
}

func NewPingTopUI(
	runtimePaths RuntimePaths,
	configManager *ConfigManager,
	stateStore *StateStore,
	logger *CSVLogger,
	coordinator *CheckCoordinator,
	updateManager *UpdateManager,
) *PingTopUI {
	ui := &PingTopUI{
		runtimePaths:  runtimePaths,
		configManager: configManager,
		stateStore:    stateStore,
		logger:        logger,
		coordinator:   coordinator,
		updateManager: updateManager,
		monitor:       NewBackgroundMonitor(configManager, stateStore, logger, coordinator),
		renderer:      termui.NewRenderer(),
		helpVisible:   configManager.Snapshot().HelpVisible,
		running:       true,
		dirty:         true,
	}
	if warning := configManager.LoadWarning(); warning != "" {
		stateStore.AddEvent("warn", warning, time.Time{})
	}
	return ui
}

func (ui *PingTopUI) Run() int {
	ui.updateManager.Start()
	ui.monitor.Start()
	ui.renderer.Enter()
	defer func() {
		ui.monitor.Stop()
		ui.updateManager.Stop()
		ui.renderer.Leave()
	}()

	inputHandler, err := termui.NewInputHandler()
	if err != nil {
		fmt.Fprintln(os.Stderr, termui.FormatInputError(err))
		return 1
	}
	defer inputHandler.Close()

	for ui.running {
		config := ui.configManager.Snapshot()
		ui.syncUpdateStatus()
		ui.renderIfNeeded(config)
		keys := inputHandler.ReadKeys(time.Duration(config.UIRefreshIntervalSeconds * float64(time.Second)))
		for _, key := range keys {
			ui.handleKey(key)
			if ui.running {
				ui.syncUpdateStatus()
				ui.renderIfNeeded(AppConfig{})
			}
		}
	}
	return 0
}

func (ui *PingTopUI) renderIfNeeded(config AppConfig) {
	if config.Version == 0 {
		config = ui.configManager.Snapshot()
	}
	snapshot := ui.stateStore.Snapshot()
	renderState := ui.buildRenderState(snapshot, config)
	if !ui.dirty && ui.hasLastRender && renderState == ui.lastRender {
		return
	}
	screen := ui.renderer.BuildScreen(
		snapshot,
		config,
		renderState.Paused,
		renderState.HelpVisible,
		ui.prompt,
		renderState.UpdateStatus,
		renderState.EventScrollOffset,
	)
	ui.renderer.Draw(screen)
	ui.lastRender = renderState
	ui.hasLastRender = true
	ui.dirty = false
}

func (ui *PingTopUI) buildRenderState(snapshot StateSnapshot, config AppConfig) uiRenderState {
	width, height := termui.TerminalSize()
	state := uiRenderState{
		StateRevision:  ui.stateStore.Revision(),
		ConfigRevision: ui.configManager.Revision(),
		UpdateStatus:   ui.updateManager.Snapshot(),
		Paused:         ui.monitor.IsPaused(),
		HelpVisible:    ui.helpVisible,
		Width:          width,
		Height:         height,
	}
	ui.eventScrollOffset, _ = ui.renderer.EventScrollState(
		snapshot,
		config,
		state.Paused,
		state.HelpVisible,
		ui.prompt,
		state.UpdateStatus,
		ui.eventScrollOffset,
	)
	state.EventScrollOffset = ui.eventScrollOffset
	if ui.prompt != nil {
		state.HasPrompt = true
		state.Prompt = *ui.prompt
	}
	return state
}

func (ui *PingTopUI) syncUpdateStatus() {
	status := ui.updateManager.Snapshot()
	if status.State == ui.lastUpdateState {
		return
	}
	ui.lastUpdateState = status.State
	ui.dirty = true
	if status.State == "available" {
		ui.stateStore.AddEvent("info", fmt.Sprintf("Update available: %s (press u to review release)", status.LatestVersion), time.Time{})
	} else if status.State == "error" {
		ui.stateStore.AddEvent("warn", "Update check failed: "+pingtop.Shorten(status.ErrorMessage, 120), time.Time{})
	}
}

func (ui *PingTopUI) handleKey(key string) {
	if key == "\x03" {
		ui.dirty = true
		ui.running = false
		return
	}
	if ui.prompt != nil {
		ui.handlePromptKey(key)
		return
	}
	if key == termui.KeyEscape {
		ui.dirty = true
		ui.running = false
		return
	}

	switch key {
	case termui.KeyUp:
		ui.scrollEvents(1)
		return
	case termui.KeyDown:
		ui.scrollEvents(-1)
		return
	case termui.KeyPageUp:
		ui.pageEvents(1)
		return
	case termui.KeyPageDown:
		ui.pageEvents(-1)
		return
	}

	switch strings.ToLower(key) {
	case "q":
		ui.dirty = true
		ui.running = false
	case "p":
		ui.dirty = true
		paused := ui.monitor.TogglePause()
		if paused {
			ui.stateStore.AddEvent("info", "Monitoring paused", time.Time{})
		} else {
			ui.stateStore.AddEvent("info", "Monitoring resumed", time.Time{})
		}
	case "l":
		ui.dirty = true
		ui.cycleLoggingMode()
	case "a":
		ui.dirty = true
		ui.prompt = &PromptState{Kind: "add", Message: "enter a hostname or IP to add"}
	case "d":
		ui.dirty = true
		ui.prompt = &PromptState{Kind: "delete", Message: "enter target index or exact target to delete"}
	case "w":
		ui.dirty = true
		ui.prompt = &PromptState{Kind: "window", Message: "duration or before,after (example 10s or 10s,20s)"}
	case "t":
		ui.dirty = true
		ui.prompt = &PromptState{Kind: "stats_window", Message: "stats window like 15m, 1h, or 1d"}
	case "r":
		ui.dirty = true
		ui.stateStore.ResetCounters()
		ui.stateStore.AddEvent("info", "Counters reset", time.Time{})
	case "s":
		ui.dirty = true
		path := ui.saveSnapshotReport()
		ui.stateStore.AddEvent("info", "Snapshot saved to "+filepath.Base(path), time.Time{})
	case "u":
		ui.dirty = true
		ui.openUpdatePage()
	case "h":
		ui.dirty = true
		config := ui.configManager.Update(func(config *pingtop.AppConfig) {
			config.HelpVisible = !ui.helpVisible
		})
		ui.helpVisible = config.HelpVisible
	default:
		switch key {
		case "+", "=":
			ui.dirty = true
			ui.adjustCheckInterval(0.5)
		case "-", "_":
			ui.dirty = true
			ui.adjustCheckInterval(-0.5)
		case "<", ",":
			ui.dirty = true
			ui.adjustUIRefresh(-0.1)
		case ">", ".":
			ui.dirty = true
			ui.adjustUIRefresh(0.1)
		}
	}
}

func (ui *PingTopUI) handlePromptKey(key string) {
	if ui.prompt == nil {
		return
	}
	switch key {
	case "\r", "\n":
		ui.dirty = true
		value := strings.TrimSpace(ui.prompt.Buffer)
		kind := ui.prompt.Kind
		ui.prompt = nil
		switch kind {
		case "add":
			ui.submitAddTarget(value)
		case "delete":
			ui.submitDeleteTarget(value)
		case "window":
			ui.submitWindow(value)
		case "stats_window":
			ui.submitStatsWindow(value)
		}
	case termui.KeyEscape:
		ui.dirty = true
		ui.prompt = nil
	case "\x7f", "\b":
		if len(ui.prompt.Buffer) > 0 {
			ui.dirty = true
			ui.prompt.Buffer = ui.prompt.Buffer[:len(ui.prompt.Buffer)-1]
		}
	default:
		if isPrintableKey(key) {
			ui.dirty = true
			ui.prompt.Buffer += key
		}
	}
}

func (ui *PingTopUI) scrollEvents(delta int) {
	if delta == 0 {
		return
	}
	ui.dirty = true
	ui.eventScrollOffset += delta
	ui.normalizeEventScroll()
}

func (ui *PingTopUI) pageEvents(direction int) {
	if direction == 0 {
		return
	}
	_, pageSize := ui.eventScrollState()
	ui.scrollEvents(direction * pageSize)
}

func (ui *PingTopUI) normalizeEventScroll() {
	clampedOffset, _ := ui.eventScrollState()
	ui.eventScrollOffset = clampedOffset
}

func (ui *PingTopUI) eventScrollState() (int, int) {
	config := ui.configManager.Snapshot()
	return ui.renderer.EventScrollState(
		ui.stateStore.Snapshot(),
		config,
		ui.monitor.IsPaused(),
		ui.helpVisible,
		ui.prompt,
		ui.updateManager.Snapshot(),
		ui.eventScrollOffset,
	)
}

func (ui *PingTopUI) cycleLoggingMode() {
	current := ui.configManager.Snapshot().LoggingMode
	index := 0
	loggingModes := pingtop.LoggingModes()
	for position, mode := range loggingModes {
		if mode == current {
			index = position
			break
		}
	}
	nextMode := loggingModes[(index+1)%len(loggingModes)]
	ui.configManager.Update(func(config *pingtop.AppConfig) {
		config.LoggingMode = nextMode
	})
	ui.monitor.Wake()
	ui.stateStore.AddEvent("info", "Logging mode set to "+nextMode, time.Time{})
}

func (ui *PingTopUI) adjustCheckInterval(delta float64) {
	current := ui.configManager.Snapshot().CheckIntervalSeconds
	next := tunedSeconds(current, delta, pingtop.CheckIntervalMinSeconds, pingtop.CheckIntervalMaxSeconds)
	if next == current {
		ui.stateStore.AddEvent(
			"info",
			fmt.Sprintf("Check interval already at %s %.2fs", tuningLimit(delta), current),
			time.Time{},
		)
		return
	}
	config := ui.configManager.Update(func(config *pingtop.AppConfig) {
		config.CheckIntervalSeconds = next
	})
	ui.monitor.Wake()
	ui.stateStore.AddEvent("info", fmt.Sprintf("Check interval set to %.2fs", config.CheckIntervalSeconds), time.Time{})
}

func (ui *PingTopUI) adjustUIRefresh(delta float64) {
	current := ui.configManager.Snapshot().UIRefreshIntervalSeconds
	next := tunedSeconds(current, delta, pingtop.UIRefreshIntervalMinSeconds, pingtop.UIRefreshIntervalMaxSeconds)
	if next == current {
		ui.stateStore.AddEvent(
			"info",
			fmt.Sprintf("UI refresh interval already at %s %.2fs", tuningLimit(delta), current),
			time.Time{},
		)
		return
	}
	config := ui.configManager.Update(func(config *pingtop.AppConfig) {
		config.UIRefreshIntervalSeconds = next
	})
	ui.stateStore.AddEvent("info", fmt.Sprintf("UI refresh interval set to %.2fs", config.UIRefreshIntervalSeconds), time.Time{})
}

func (ui *PingTopUI) submitAddTarget(raw string) {
	if raw == "" {
		ui.stateStore.AddEvent("warn", "Add target canceled: empty input", time.Time{})
		return
	}
	target, err := pingtop.InferTarget(raw)
	if err != nil {
		ui.stateStore.AddEvent("warn", "Invalid target: "+err.Error(), time.Time{})
		return
	}
	config := ui.configManager.Update(func(config *pingtop.AppConfig) {
		config.Targets = append(config.Targets, target)
	})
	ui.stateStore.SyncTargets(config)
	ui.monitor.Wake()
	ui.stateStore.AddEvent("info", "Added target "+target.Value, time.Time{})
}

func (ui *PingTopUI) submitDeleteTarget(raw string) {
	if raw == "" {
		ui.stateStore.AddEvent("warn", "Delete target canceled: empty input", time.Time{})
		return
	}
	configSnapshot := ui.configManager.Snapshot()
	targetToRemove := ""
	if index, err := strconv.Atoi(raw); err == nil {
		if index >= 1 && index <= len(configSnapshot.Targets) {
			targetToRemove = configSnapshot.Targets[index-1].Value
		}
	} else if target, err := pingtop.InferTarget(raw); err == nil {
		targetToRemove = target.Value
	} else {
		targetToRemove = strings.ToLower(strings.TrimSpace(raw))
	}

	if targetToRemove == "" {
		ui.stateStore.AddEvent("warn", "Delete target failed: no match for "+raw, time.Time{})
		return
	}

	removed := false
	config := ui.configManager.Update(func(config *pingtop.AppConfig) {
		kept := make([]TargetSpec, 0, len(config.Targets))
		for _, target := range config.Targets {
			if target.Value == targetToRemove {
				removed = true
				continue
			}
			kept = append(kept, target)
		}
		config.Targets = kept
	})
	ui.stateStore.SyncTargets(config)
	if removed {
		ui.monitor.Wake()
		ui.stateStore.AddEvent("info", "Deleted target "+targetToRemove, time.Time{})
	} else {
		ui.stateStore.AddEvent("warn", "Delete target failed: no match for "+raw, time.Time{})
	}
}

func (ui *PingTopUI) submitWindow(raw string) {
	if raw == "" {
		ui.stateStore.AddEvent("warn", "Around-failure window unchanged", time.Time{})
		return
	}
	beforeValue := 0
	afterValue := 0
	var err error
	if strings.Contains(raw, ",") {
		parts := strings.SplitN(raw, ",", 2)
		beforeValue, err = pingtop.ParseDurationInput(strings.TrimSpace(parts[0]))
		if err == nil {
			afterValue, err = pingtop.ParseDurationInput(strings.TrimSpace(parts[1]))
		}
	} else {
		beforeValue, err = pingtop.ParseDurationInput(raw)
		afterValue = beforeValue
	}
	if err != nil {
		ui.stateStore.AddEvent("warn", "Window must be a duration like 10s or 10s,20s", time.Time{})
		return
	}
	config := ui.configManager.Update(func(config *pingtop.AppConfig) {
		config.AroundFailureBefore = beforeValue
		config.AroundFailureAfter = afterValue
	})
	ui.monitor.Wake()
	ui.stateStore.AddEvent(
		"info",
		fmt.Sprintf("Around-failure window set to %d/%ds", config.AroundFailureBefore, config.AroundFailureAfter),
		time.Time{},
	)
}

func (ui *PingTopUI) submitStatsWindow(raw string) {
	if raw == "" {
		ui.stateStore.AddEvent("warn", "Stats window unchanged", time.Time{})
		return
	}
	statsWindowSeconds, err := pingtop.ParseDurationInput(raw)
	if err != nil {
		ui.stateStore.AddEvent("warn", "Stats window error: "+err.Error(), time.Time{})
		return
	}
	config := ui.configManager.Update(func(config *pingtop.AppConfig) {
		config.StatsWindowSeconds = statsWindowSeconds
	})
	if ui.stateStore.SyncTargets(config) {
		ui.monitor.Wake()
		ui.stateStore.AddEvent(
			"info",
			fmt.Sprintf("Stats window set to %s; rolling counters reset", pingtop.FormatCompactSpan(config.StatsWindowSeconds)),
			time.Time{},
		)
	}
}

func (ui *PingTopUI) saveSnapshotReport() string {
	snapshot := ui.stateStore.Snapshot()
	config := ui.configManager.Snapshot()
	path := ui.runtimePaths.SnapshotPath(time.Time{})
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(ui.renderer.BuildReport(snapshot, config, ui.monitor.IsPaused())), 0o644)
	return path
}

func (ui *PingTopUI) openUpdatePage() {
	status := ui.updateManager.Snapshot()
	ok, message := ui.updateManager.OpenPage()
	if ok {
		if status.IsAvailable() && status.LatestVersion != "" {
			ui.stateStore.AddEvent("info", "Opened release page for "+status.LatestVersion, time.Time{})
		} else {
			ui.stateStore.AddEvent("info", "Opened project updates page", time.Time{})
		}
		return
	}
	ui.stateStore.AddEvent("warn", message, time.Time{})
}

func isPrintableKey(key string) bool {
	if key == "" {
		return false
	}
	runes := []rune(key)
	if len(runes) != 1 {
		return false
	}
	return runes[0] >= 32 && runes[0] != 127
}

func roundTo(value float64, places int) float64 {
	factor := math.Pow(10, float64(places))
	return math.Round(value*factor) / factor
}

func tunedSeconds(current, delta, minimum, maximum float64) float64 {
	return roundTo(math.Min(maximum, math.Max(minimum, current+delta)), 2)
}

func tuningLimit(delta float64) string {
	if delta < 0 {
		return "minimum"
	}
	return "maximum"
}
