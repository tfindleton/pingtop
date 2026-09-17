package app

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfindleton/pingtop/internal/checks"
	"github.com/tfindleton/pingtop/internal/pingtop"
)

func newReviewTestUI(t *testing.T) *PingTopUI {
	t.Helper()
	directory := t.TempDir()
	services, err := buildServices(RuntimePaths{
		RuntimeDir: directory,
		ConfigPath: filepath.Join(directory, "pingtop.json"),
		LogPath:    filepath.Join(directory, "pingtop.csv"),
	}, cliArgs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.coordinator.Close)
	return NewPingTopUI(services.runtimePaths, services.configManager, services.stateStore,
		services.logger, services.coordinator, services.updateManager)
}

func TestUIRefreshesStaleStatusWithoutStateChanges(t *testing.T) {
	ui := newReviewTestUI(t)
	config := ui.configManager.Update(func(config *AppConfig) {
		config.Targets = []TargetSpec{{Value: "192.0.2.1", Kind: "ip"}}
		config.HelpVisible = false
		config.UIRefreshIntervalSeconds = 0.1
	})
	ui.helpVisible = false
	staleAfter := config.CheckIntervalSeconds + float64(config.PingTimeoutMS)/1000 + 1
	ui.stateStore.HandleCycle([]CheckResult{{
		Target: "192.0.2.1", TargetType: "ip", PingSuccess: true, ErrorCategory: "ok",
		Timestamp: time.Now().Add(-time.Duration(staleAfter * float64(time.Second))).Add(time.Second),
	}}, config, 1)
	first := captureStdout(t, func() int {
		ui.renderIfNeeded(config)
		return 0
	})
	if strings.Contains(first, "stale") {
		t.Fatalf("target was already stale before deadline: %s", first)
	}
	time.Sleep(1100 * time.Millisecond)
	second := captureStdout(t, func() int {
		ui.renderIfNeeded(config)
		return 0
	})
	if !strings.Contains(second, "stale") {
		t.Fatalf("expected time alone to refresh stale status, got %q", second)
	}
}

func TestSnapshotSaveFailureIsReported(t *testing.T) {
	ui := newReviewTestUI(t)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui.runtimePaths.RuntimeDir = blocker
	ui.handleKey("s")
	events := ui.stateStore.Snapshot().RecentEvents
	if len(events) == 0 || events[len(events)-1].Level != "warn" ||
		!strings.Contains(events[len(events)-1].Message, "Snapshot save failed:") {
		t.Fatalf("expected snapshot failure warning, got %#v", events)
	}
}

func TestMonitorReportsLogErrorsWithoutFloodingEvents(t *testing.T) {
	ui := newReviewTestUI(t)
	config := ui.configManager.Update(func(config *AppConfig) {
		config.LoggingMode = pingtop.LoggingModeAll
	})
	blocker := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui.monitor.logger = pingtop.NewCSVLogger(filepath.Join(blocker, "pingtop.csv"))
	results := []CheckResult{{Target: "example.com", Timestamp: time.Now(), ErrorCategory: "dns_failure"}}
	if err := ui.monitor.logResults(results, config); err == nil {
		t.Fatal("expected initial logging error")
	}
	if err := ui.monitor.logResults(results, config); err != nil {
		t.Fatalf("expected repeated logging error to be suppressed, got %v", err)
	}
	events := ui.stateStore.Snapshot().RecentEvents
	if len(events) != 1 || events[0].Level != "warn" || !strings.Contains(events[0].Message, "CSV log") {
		t.Fatalf("expected one visible logging warning, got %#v", events)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := ui.monitor.logResults(nil, config); err != nil {
		t.Fatalf("expected logging retry to recover: %v", err)
	}
	if ui.monitor.lastLogError != "" {
		t.Fatal("expected recovery to clear warning suppression")
	}
}

func TestMonitorConfigurationChangeDiscardsCanceledChecks(t *testing.T) {
	ui := newReviewTestUI(t)
	config, _ := ui.monitor.updateConfig(func(config *AppConfig) {
		config.Targets = []TargetSpec{{Value: "old.example", Kind: "hostname"}}
		config.PingTimeoutMS = 10000
		config.CheckIntervalSeconds = 300
	})
	started := make(chan struct{}, 1)
	resolver := checks.NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		started <- struct{}{}
		<-ctx.Done()
		return false, "", ctx.Err().Error()
	})
	coordinator := checks.NewCheckCoordinator(checks.NewPingRunner(), resolver)
	defer coordinator.Close()
	monitor := NewBackgroundMonitor(ui.configManager, ui.stateStore, pingtop.NewDisabledCSVLogger(), coordinator)
	monitor.Start()
	defer monitor.Stop()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("monitor did not begin check")
	}
	monitor.updateConfig(func(config *AppConfig) {
		config.Targets = nil
		config.StatsWindowSeconds *= 2
	})
	waitForCycles(t, ui.stateStore, 1)
	monitor.Stop()
	snapshot := ui.stateStore.Snapshot()
	if len(snapshot.TargetStats) != 0 || snapshot.ActiveCycle.Active || snapshot.Session.TotalChecks != 0 {
		t.Fatalf("canceled check restored removed target or added failure: %#v", snapshot)
	}
	if snapshot.StatsWindowSeconds != config.StatsWindowSeconds*2 {
		t.Fatalf("canceled check restored old stats window: %d", snapshot.StatsWindowSeconds)
	}
}

func TestSingleCycleCanBeInterrupted(t *testing.T) {
	ui := newReviewTestUI(t)
	config := ui.configManager.Update(func(config *AppConfig) {
		config.Targets = []TargetSpec{{Value: "slow.example", Kind: "hostname"}}
		config.PingTimeoutMS = 10000
	})
	started := make(chan struct{})
	resolver := checks.NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		close(started)
		<-ctx.Done()
		return false, "", ctx.Err().Error()
	})
	coordinator := checks.NewCheckCoordinator(checks.NewPingRunner(), resolver)
	defer coordinator.Close()
	monitor := NewBackgroundMonitor(ui.configManager, ui.stateStore, pingtop.NewDisabledCSVLogger(), coordinator)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan []CheckResult, 1)
	go func() {
		finished <- monitor.RunSingleCycleContext(ctx, config)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("single cycle did not begin check")
	}
	cancel()
	select {
	case results := <-finished:
		if len(results) != 0 {
			t.Fatalf("canceled cycle returned failures: %#v", results)
		}
	case <-time.After(time.Second):
		t.Fatal("single cycle ignored cancellation")
	}
}

func TestInterruptedCycleRecordsCompletedFailuresWithoutCompletingCycle(t *testing.T) {
	ui := newReviewTestUI(t)
	config := ui.configManager.Update(func(config *AppConfig) {
		config.Targets = []TargetSpec{
			{Value: "fast.example", Kind: "hostname"},
			{Value: "slow.example", Kind: "hostname"},
		}
		config.LoggingMode = pingtop.LoggingModeFailuresOnly
		config.DiagnosisConfirmCycles = 1
	})
	ui.stateStore.SyncTargets(config)
	path := filepath.Join(t.TempDir(), "results.csv")
	ui.monitor.logger = pingtop.NewCSVLogger(path)
	results := []CheckResult{{
		Target: "fast.example", TargetType: "hostname", Timestamp: time.Now(),
		ErrorCategory: "dns_failure", DNSSuccess: new(bool),
	}}
	if err := ui.monitor.recordSingleCycle(results, config, true); err != nil {
		t.Fatal(err)
	}
	snapshot := ui.stateStore.Snapshot()
	if snapshot.Session.TotalChecks != 1 || snapshot.Session.Failures != 1 || snapshot.Session.CyclesCompleted != 0 {
		t.Fatalf("interrupted cycle lost the completed failure or counted an incomplete cycle: %#v", snapshot.Session)
	}
	if snapshot.Diagnosis != "Waiting for first cycle" || snapshot.TargetStats[1].TotalChecks != 0 {
		t.Fatalf("interrupted cycle changed diagnosis or invented a result for the unfinished target: %#v", snapshot)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][1] != "fast.example" || rows[1][7] != "dns_failure" {
		t.Fatalf("expected exactly the completed failure in CSV, got %#v", rows)
	}
}

func TestLoggingOffWhilePausedDiscardsPendingResults(t *testing.T) {
	ui := newReviewTestUI(t)
	config := ui.configManager.Update(func(config *AppConfig) {
		config.LoggingMode = pingtop.LoggingModeAll
	})
	directory := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(directory, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "results.csv")
	ui.monitor.logger = pingtop.NewCSVLogger(path)
	if err := ui.monitor.logResults([]CheckResult{{
		Target: "old.example", TargetType: "hostname", Timestamp: time.Now(), ErrorCategory: "dns_failure",
	}}, config); err == nil {
		t.Fatal("expected the original failure to be queued after a write error")
	}
	ui.monitor.TogglePause()
	ui.cycleLoggingMode() // all -> off, with no active workers to notify the logger
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	ui.cycleLoggingMode() // off -> around_failure
	config = ui.configManager.Snapshot()
	if err := ui.monitor.logResults([]CheckResult{{
		Target: "new.example", TargetType: "hostname", Timestamp: time.Now(), ErrorCategory: "dns_failure",
	}}, config); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][1] != "new.example" {
		t.Fatalf("switching logging off retained old queued results: %#v", rows)
	}
}

func TestMonitorCancellationDoesNotWaitForLogLock(t *testing.T) {
	ui := newReviewTestUI(t)
	config := ui.configManager.Update(func(config *AppConfig) {
		config.Targets = []TargetSpec{{Value: "example.test", Kind: "hostname"}}
	})
	lookupStarted := make(chan struct{})
	resolver := checks.NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		close(lookupStarted)
		return false, "", "lookup failed"
	})
	coordinator := checks.NewCheckCoordinator(checks.NewPingRunner(), resolver)
	defer coordinator.Close()
	monitor := NewBackgroundMonitor(ui.configManager, ui.stateStore, pingtop.NewDisabledCSVLogger(), coordinator)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor.cancelCycle = cancel
	monitor.logMu.Lock()
	locked := true
	defer func() {
		if locked {
			monitor.logMu.Unlock()
		}
	}()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		monitor.runTargetWorker(ctx, config, config.Targets[0], 0, 1, "worker-1")
	}()
	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin its check")
	}
	// A delayed preceding append must also delay accepting newer results. It
	// must not make ForceRefresh wait for filesystem I/O before canceling them.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if checks := ui.stateStore.Snapshot().Session.TotalChecks; checks != 0 {
			t.Fatalf("accepted %d checks before logging order was available", checks)
		}
		time.Sleep(time.Millisecond)
	}
	refreshed := make(chan struct{})
	go func() {
		monitor.ForceRefresh()
		close(refreshed)
	}()
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for the logger")
	}
	monitor.logMu.Unlock()
	locked = false
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("canceled worker did not finish after logger was released")
	}
	if checks := ui.stateStore.Snapshot().Session.TotalChecks; checks != 0 {
		t.Fatalf("canceled worker committed %d checks", checks)
	}
}
