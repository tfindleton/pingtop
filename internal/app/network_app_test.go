package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfindleton/pingtop/internal/checks"
	"github.com/tfindleton/pingtop/internal/pingtop"
	termui "github.com/tfindleton/pingtop/internal/ui"
)

func TestRunHeadlessOncePrintsSummary(t *testing.T) {
	tempDir := t.TempDir()
	configManager := pingtop.NewConfigManager(filepath.Join(tempDir, "pingtop.json"))
	config := configManager.Update(func(config *AppConfig) {
		config.Targets = nil
	})
	stateStore := pingtop.NewStateStore(config)
	logger := pingtop.NewCSVLogger(filepath.Join(tempDir, "pingtop_log.csv"))
	coordinator := checks.NewCheckCoordinator(checks.NewPingRunner(), nil)
	defer coordinator.Close()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	rc := runHeadless(configManager, stateStore, logger, coordinator, true)
	_ = writer.Close()
	os.Stdout = originalStdout

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read captured stdout: %v", err)
	}
	if rc != 0 {
		t.Fatalf("expected rc 0, got %d", rc)
	}
	if !strings.Contains(string(output), "Session summary:") {
		t.Fatalf("expected summary output, got %q", string(output))
	}
}

func TestRunLongHelpPrintsUsage(t *testing.T) {
	output := captureStdout(t, func() int {
		return Run([]string{"--help"})
	})
	if !strings.Contains(output, "Usage: pingtop [flags]") {
		t.Fatalf("expected usage output, got %q", output)
	}
}

func TestRunShortHelpPrintsUsage(t *testing.T) {
	output := captureStdout(t, func() int {
		return Run([]string{"-h"})
	})
	if !strings.Contains(output, "Usage: pingtop [flags]") {
		t.Fatalf("expected usage output, got %q", output)
	}
}

func TestRunShortVersionPrintsVersion(t *testing.T) {
	output := captureStdout(t, func() int {
		return Run([]string{"-v"})
	})
	if !strings.Contains(output, pingtop.Version) {
		t.Fatalf("expected version output, got %q", output)
	}
}

func TestParseArgsSupportsShortAliases(t *testing.T) {
	args, err := parseArgs([]string{"-n", "-o", "-v"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !args.noUI {
		t.Fatal("expected -n to enable no-ui mode")
	}
	if !args.once {
		t.Fatal("expected -o to enable once mode")
	}
	if !args.showVersion {
		t.Fatal("expected -v to enable version output")
	}
}

func TestParseArgsSupportsUpdateCheckFlags(t *testing.T) {
	args, err := parseArgs([]string{"--check-updates", "--update-repo", "https://github.com/tfindleton/pingtop", "--current-version", "0.1.3"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !args.checkUpdates {
		t.Fatal("expected --check-updates to enable update check mode")
	}
	if args.updateRepo != "https://github.com/tfindleton/pingtop" {
		t.Fatalf("unexpected update repo: %q", args.updateRepo)
	}
	if args.forceVersion != "0.1.3" {
		t.Fatalf("unexpected current version override: %q", args.forceVersion)
	}
}

func TestParseArgsSupportsShortUpdateAlias(t *testing.T) {
	args, err := parseArgs([]string{"-u", "--current-version", "0.1.3"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !args.checkUpdates {
		t.Fatal("expected -u to enable update check mode")
	}
	if args.forceVersion != "0.1.3" {
		t.Fatalf("unexpected current version override: %q", args.forceVersion)
	}
}

func TestParseArgsSupportsLongUpdatesAlias(t *testing.T) {
	args, err := parseArgs([]string{"--updates", "--current-version", "0.1.3"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !args.checkUpdates {
		t.Fatal("expected --updates to enable update check mode")
	}
	if args.forceVersion != "0.1.3" {
		t.Fatalf("unexpected current version override: %q", args.forceVersion)
	}
}

func TestParseArgsCapturesPositionalTargets(t *testing.T) {
	args, err := parseArgs([]string{"-n", "example.com", "1.1.1.1"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !args.noUI {
		t.Fatal("expected -n to enable no-ui mode")
	}
	if len(args.targets) != 2 || args.targets[0] != "example.com" || args.targets[1] != "1.1.1.1" {
		t.Fatalf("unexpected targets: %#v", args.targets)
	}
}

func TestBuildServicesWithPositionalTargetsDisablesLogging(t *testing.T) {
	tempDir := t.TempDir()
	runtimePaths := pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}
	services, err := buildServices(runtimePaths, cliArgs{targets: []string{"example.com", "1.1.1.1"}})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	config := services.configManager.Snapshot()
	if len(config.Targets) != 2 || config.Targets[0].Value != "example.com" || config.Targets[1].Value != "1.1.1.1" {
		t.Fatalf("unexpected configured targets: %#v", config.Targets)
	}

	success := true
	latency := 12.0
	services.logger.LogResults([]CheckResult{{
		CycleID:       1,
		Timestamp:     pingtop.NewSessionTotals().StartedAt,
		Target:        "example.com",
		TargetType:    "hostname",
		ResolvedIP:    "93.184.216.34",
		DNSSuccess:    &success,
		PingSuccess:   true,
		LatencyMS:     &latency,
		ErrorCategory: "ok",
	}}, config)

	if _, err := os.Stat(runtimePaths.LogPath); !os.IsNotExist(err) {
		t.Fatalf("expected positional-target run to avoid creating log file, got err=%v", err)
	}
}

func TestBackgroundMonitorWakeRunsBeforeLongInterval(t *testing.T) {
	tempDir := t.TempDir()
	configManager := pingtop.NewConfigManager(filepath.Join(tempDir, "pingtop.json"))
	config := configManager.Update(func(config *AppConfig) {
		config.Targets = nil
		config.CheckIntervalSeconds = 300
	})
	stateStore := pingtop.NewStateStore(config)
	logger := pingtop.NewDisabledCSVLogger()
	coordinator := checks.NewCheckCoordinator(checks.NewPingRunner(), nil)
	defer coordinator.Close()

	monitor := NewBackgroundMonitor(configManager, stateStore, logger, coordinator)
	monitor.Start()
	defer monitor.Stop()

	waitForCycles(t, stateStore, 1)
	monitor.Wake()
	waitForCycles(t, stateStore, 2)
}

func TestAdjustCheckIntervalWakesMonitor(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)
	ui.adjustCheckInterval(-0.5)

	if len(ui.monitor.wakeCh) != 1 {
		t.Fatal("expected check interval tuning to wake the background monitor")
	}
}

func TestRefreshKeysDecreaseAndIncreaseRefreshInterval(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)

	ui.handleKey("<")
	if got := services.configManager.Snapshot().UIRefreshIntervalSeconds; got != 0.4 {
		t.Fatalf("expected < to decrease refresh interval to 0.40s, got %.2f", got)
	}

	ui.handleKey(">")
	if got := services.configManager.Snapshot().UIRefreshIntervalSeconds; got != 0.5 {
		t.Fatalf("expected > to increase refresh interval to 0.50s, got %.2f", got)
	}
}

func TestRefreshTuningReportsBounds(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)

	services.configManager.Update(func(config *AppConfig) {
		config.UIRefreshIntervalSeconds = pingtop.UIRefreshIntervalMinSeconds
	})
	ui.handleKey("<")
	if got := services.configManager.Snapshot().UIRefreshIntervalSeconds; got != pingtop.UIRefreshIntervalMinSeconds {
		t.Fatalf("expected refresh interval to stay at min, got %.2f", got)
	}
	if message := latestEventMessage(services.stateStore); !strings.Contains(message, "already at minimum 0.10s") {
		t.Fatalf("expected minimum message, got %q", message)
	}

	services.configManager.Update(func(config *AppConfig) {
		config.UIRefreshIntervalSeconds = pingtop.UIRefreshIntervalMaxSeconds
	})
	ui.handleKey(">")
	if got := services.configManager.Snapshot().UIRefreshIntervalSeconds; got != pingtop.UIRefreshIntervalMaxSeconds {
		t.Fatalf("expected refresh interval to stay at max, got %.2f", got)
	}
	if message := latestEventMessage(services.stateStore); !strings.Contains(message, "already at maximum 5.00s") {
		t.Fatalf("expected maximum message, got %q", message)
	}
}

func TestCheckIntervalTuningReportsBounds(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)

	services.configManager.Update(func(config *AppConfig) {
		config.CheckIntervalSeconds = pingtop.CheckIntervalMinSeconds
	})
	ui.handleKey("-")
	if got := services.configManager.Snapshot().CheckIntervalSeconds; got != pingtop.CheckIntervalMinSeconds {
		t.Fatalf("expected check interval to stay at min, got %.2f", got)
	}
	if message := latestEventMessage(services.stateStore); !strings.Contains(message, "already at minimum 0.50s") {
		t.Fatalf("expected minimum message, got %q", message)
	}
}

func TestHandleKeyEscQuitsWhenNoPromptIsActive(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)
	ui.handleKey(termui.KeyEscape)

	if ui.running {
		t.Fatal("expected Esc to stop the UI when no prompt is active")
	}
}

func TestHandleKeyEscCancelsPromptWithoutQuitting(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)
	ui.prompt = &PromptState{Kind: "add", Message: "test"}
	ui.handleKey(termui.KeyEscape)

	if !ui.running {
		t.Fatal("expected Esc to keep the UI running while canceling a prompt")
	}
	if ui.prompt != nil {
		t.Fatal("expected Esc to clear the active prompt")
	}
}

func TestNewPingTopUIDefaultsHelpVisibleFromConfig(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)

	if !ui.helpVisible {
		t.Fatal("expected help to be visible by default")
	}
}

func TestHandleKeyHTogglesAndPersistsHelpVisibility(t *testing.T) {
	tempDir := t.TempDir()
	services, err := buildServices(pingtop.RuntimePaths{
		ConfigPath: filepath.Join(tempDir, "pingtop.json"),
		LogPath:    filepath.Join(tempDir, "pingtop_log.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatalf("unexpected buildServices error: %v", err)
	}
	defer services.coordinator.Close()

	ui := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)

	ui.handleKey("h")
	if ui.helpVisible {
		t.Fatal("expected h to hide help")
	}
	if services.configManager.Snapshot().HelpVisible {
		t.Fatal("expected help visibility change to persist to config")
	}

	reopened := NewPingTopUI(
		services.runtimePaths,
		services.configManager,
		services.stateStore,
		services.logger,
		services.coordinator,
		services.updateManager,
	)
	if reopened.helpVisible {
		t.Fatal("expected reopened UI to use saved hidden help state")
	}
}

func TestRunUpdateCheckPrintsAvailableStatus(t *testing.T) {
	original := checkUpdatesNow
	checkUpdatesNow = func(currentVersion, repoURL string, enabled bool) UpdateStatus {
		return UpdateStatus{
			State:          "available",
			CurrentVersion: currentVersion,
			LatestVersion:  "0.1.4",
			RepoURL:        repoURL,
			ReleaseURL:     repoURL + "/releases/tag/0.1.4",
		}
	}
	defer func() {
		checkUpdatesNow = original
	}()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stderr pipe: %v", err)
	}

	rc := runUpdateCheck(stdoutWriter, stderrWriter, cliArgs{
		checkUpdates: true,
		updateRepo:   "https://github.com/tfindleton/pingtop",
		forceVersion: "0.1.3",
	})
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	if rc != 0 {
		t.Fatalf("expected rc 0, got %d", rc)
	}

	stdout, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	stderr, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatalf("failed to read stderr: %v", err)
	}
	if len(stderr) != 0 {
		t.Fatalf("expected no stderr output, got %q", string(stderr))
	}
	text := string(stdout)
	if !strings.Contains(text, "state: available") || !strings.Contains(text, "latest version: 0.1.4") {
		t.Fatalf("unexpected output: %q", text)
	}
}

func TestRunShortUpdateAliasPrintsAvailableStatus(t *testing.T) {
	original := checkUpdatesNow
	checkUpdatesNow = func(currentVersion, repoURL string, enabled bool) UpdateStatus {
		return UpdateStatus{
			State:          "available",
			CurrentVersion: currentVersion,
			LatestVersion:  "0.1.4",
			RepoURL:        repoURL,
			ReleaseURL:     repoURL + "/releases/tag/0.1.4",
		}
	}
	defer func() {
		checkUpdatesNow = original
	}()

	output := captureStdout(t, func() int {
		return Run([]string{"-u", "--current-version", "0.1.3", "--update-repo", "https://github.com/tfindleton/pingtop"})
	})
	if !strings.Contains(output, "state: available") || !strings.Contains(output, "latest version: 0.1.4") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func waitForCycles(t *testing.T, stateStore *StateStore, expected int) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if stateStore.Snapshot().Session.CyclesCompleted >= expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d monitor cycles, got %d", expected, stateStore.Snapshot().Session.CyclesCompleted)
}

func latestEventMessage(stateStore *StateStore) string {
	events := stateStore.Snapshot().RecentEvents
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Message
}

func captureStdout(t *testing.T, run func() int) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	defer reader.Close()

	originalStdout := os.Stdout
	os.Stdout = writer
	rc := run()
	_ = writer.Close()
	os.Stdout = originalStdout

	if rc != 0 {
		t.Fatalf("expected rc 0, got %d", rc)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read captured stdout: %v", err)
	}
	return string(output)
}
