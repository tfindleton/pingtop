package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

func TestBuildExitSummaryIncludesTitleAndRuntime(t *testing.T) {
	renderer := &Renderer{}
	startedAt := time.Unix(100, 0)
	completedAt := time.Unix(225, 0)
	output := renderer.BuildExitSummary(pingtop.StateSnapshot{
		Diagnosis:            "All monitored targets are reachable",
		LastCycleCompletedAt: completedAt,
		Session: pingtop.SessionTotals{
			StartedAt:       startedAt,
			CyclesCompleted: 2,
			TotalChecks:     10,
			Successes:       10,
		},
	})

	if !strings.Contains(output, "pingtop  Session summary") {
		t.Fatalf("expected title line, got %q", output)
	}
	if !strings.Contains(output, "ran 2m5s") {
		t.Fatalf("expected runtime in output, got %q", output)
	}
}

func TestBuildScreenIncludesVersionInHeader(t *testing.T) {
	renderer := &Renderer{}
	output := renderer.BuildScreen(
		pingtop.StateSnapshot{
			Diagnosis:          "All monitored targets are reachable",
			StatsWindowSeconds: 3600,
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
		},
		false,
		false,
		nil,
		UpdateStatus{},
		0,
	)

	if !strings.Contains(output, "pingtop "+pingtop.Version) {
		t.Fatalf("expected versioned header, got %q", output)
	}
}

func TestBuildScreenOmitsPromptHelpWhenNoPromptIsActive(t *testing.T) {
	renderer := &Renderer{}
	output := renderer.BuildScreen(
		pingtop.StateSnapshot{
			Diagnosis:          "All monitored targets are reachable",
			StatsWindowSeconds: 3600,
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			HelpVisible:              true,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
		},
		false,
		true,
		nil,
		UpdateStatus{},
		0,
	)

	if strings.Contains(output, "Enter submit") {
		t.Fatalf("expected prompt help to stay hidden without an active prompt, got %q", output)
	}
}

func TestBuildScreenShowsPromptHelpInsideActivePrompt(t *testing.T) {
	renderer := &Renderer{}
	output := renderer.BuildScreen(
		pingtop.StateSnapshot{
			Diagnosis:          "All monitored targets are reachable",
			StatsWindowSeconds: 3600,
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			HelpVisible:              true,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
		},
		false,
		true,
		&PromptState{Kind: "add", Message: "enter a hostname or IP to add"},
		UpdateStatus{},
		0,
	)

	if !strings.Contains(output, "Enter submit | Esc cancel | Backspace edit") {
		t.Fatalf("expected active prompt to include prompt help, got %q", output)
	}
}

func TestBuildScreenKeepsFooterTargetsVisibleWhenHeaderWraps(t *testing.T) {
	originalTerminalSize := terminalSize
	terminalSize = func() (int, int) {
		return 40, 20
	}
	defer func() {
		terminalSize = originalTerminalSize
	}()

	renderer := &Renderer{}
	config := pingtop.AppConfig{
		CheckIntervalSeconds:     1,
		PingTimeoutMS:            1200,
		UIRefreshIntervalSeconds: 0.5,
		HelpVisible:              true,
		StatsWindowSeconds:       3600,
		LatencyWarningMS:         100,
		LatencyCriticalMS:        250,
		DiagnosisConfirmCycles:   2,
		RecoveryConfirmCycles:    2,
		LoggingMode:              "around_failure",
		LogRotationMaxMB:         25,
		LogRotationKeepFiles:     10,
		VisibleEventLines:        8,
		Targets: []pingtop.TargetSpec{
			{Value: "1.1.1.1", Kind: "ip"},
			{Value: "8.8.8.8", Kind: "ip"},
		},
	}
	snapshot := pingtop.StateSnapshot{
		Diagnosis:          "All monitored targets are reachable",
		StatsWindowSeconds: 3600,
	}

	current := renderer.BuildScreen(
		snapshot,
		config,
		false,
		true,
		nil,
		UpdateStatus{State: "current"},
		0,
	)
	available := renderer.BuildScreen(
		snapshot,
		config,
		false,
		true,
		nil,
		UpdateStatus{State: "available", LatestVersion: "v0.1.6"},
		0,
	)

	assertFooterTargetsLine := func(output string) {
		lines := strings.Split(output, "\n")
		if len(lines) != 20 {
			t.Fatalf("expected output to fill terminal height, got %d lines in %q", len(lines), output)
		}
		footerFound := false
		for _, line := range lines[len(lines)-10:] {
			if strings.Contains(line, "[a] add") && strings.Contains(line, "[d] delete") {
				footerFound = true
				break
			}
		}
		if !footerFound {
			t.Fatalf("expected targets footer line near the bottom, got %q", output)
		}
	}

	assertFooterTargetsLine(current)
	assertFooterTargetsLine(available)
}

func TestBuildScreenScrollsOlderEvents(t *testing.T) {
	originalTerminalSize := terminalSize
	terminalSize = func() (int, int) {
		return 80, 20
	}
	defer func() {
		terminalSize = originalTerminalSize
	}()

	renderer := &Renderer{}
	events := make([]pingtop.EventEntry, 0, 10)
	for index := 1; index <= 10; index++ {
		events = append(events, pingtop.EventEntry{
			Timestamp: time.Unix(int64(index), 0),
			Level:     "warn",
			Message:   "event " + strconv.Itoa(index),
		})
	}

	config := pingtop.AppConfig{
		CheckIntervalSeconds:     1,
		PingTimeoutMS:            1200,
		UIRefreshIntervalSeconds: 0.5,
		StatsWindowSeconds:       3600,
		LatencyWarningMS:         100,
		LatencyCriticalMS:        250,
		DiagnosisConfirmCycles:   2,
		RecoveryConfirmCycles:    2,
		LoggingMode:              "around_failure",
		VisibleEventLines:        3,
	}
	snapshot := pingtop.StateSnapshot{
		Diagnosis:          "All monitored targets are reachable",
		StatsWindowSeconds: 3600,
		RecentEvents:       events,
	}

	_, newestView := renderer.buildScreenLayout(snapshot, config, false, false, UpdateStatus{}, 0)
	_, olderView := renderer.buildScreenLayout(snapshot, config, false, false, UpdateStatus{}, 2)
	newest := renderer.BuildScreen(snapshot, config, false, false, nil, UpdateStatus{}, 0)
	older := renderer.BuildScreen(snapshot, config, false, false, nil, UpdateStatus{}, 2)

	if !strings.Contains(newest, newestView.summary()) || !strings.Contains(newest, "event 10") {
		t.Fatalf("expected newest view to show latest events, got %q", newest)
	}
	if !strings.Contains(older, olderView.summary()) || !strings.Contains(older, "event "+strconv.Itoa(olderView.start+1)) {
		t.Fatalf("expected scrolled view to show older events, got %q", older)
	}
	if strings.Contains(older, "event 10") {
		t.Fatalf("expected scrolled view to exclude the newest event, got %q", older)
	}
}

func TestEventPaneExpandsBeyondConfiguredVisibleEventLines(t *testing.T) {
	originalTerminalSize := terminalSize
	terminalSize = func() (int, int) {
		return 80, 28
	}
	defer func() {
		terminalSize = originalTerminalSize
	}()

	renderer := &Renderer{}
	events := make([]pingtop.EventEntry, 0, 20)
	for index := 1; index <= 20; index++ {
		events = append(events, pingtop.EventEntry{
			Timestamp: time.Unix(int64(index), 0),
			Level:     "warn",
			Message:   "event " + strconv.Itoa(index),
		})
	}

	config := pingtop.AppConfig{
		CheckIntervalSeconds:     1,
		PingTimeoutMS:            1200,
		UIRefreshIntervalSeconds: 0.5,
		StatsWindowSeconds:       3600,
		LatencyWarningMS:         100,
		LatencyCriticalMS:        250,
		DiagnosisConfirmCycles:   2,
		RecoveryConfirmCycles:    2,
		LoggingMode:              "around_failure",
		VisibleEventLines:        3,
	}
	snapshot := pingtop.StateSnapshot{
		Diagnosis:          "All monitored targets are reachable",
		StatsWindowSeconds: 3600,
		RecentEvents:       events,
	}

	_, view := renderer.buildScreenLayout(snapshot, config, false, false, UpdateStatus{}, 0)
	if view.availableLines <= config.VisibleEventLines {
		t.Fatalf("expected event pane to expand beyond %d lines, got %d", config.VisibleEventLines, view.availableLines)
	}
}

func TestClearBelowAllowedSkipsWhenScreenIsFull(t *testing.T) {
	if clearBelowAllowed(20, 20) {
		t.Fatal("expected clear-below to be skipped when content fills the terminal")
	}
}

func TestClearBelowAllowedRunsWhenRowsRemain(t *testing.T) {
	if !clearBelowAllowed(19, 20) {
		t.Fatal("expected clear-below to run when content does not fill the terminal")
	}
}
