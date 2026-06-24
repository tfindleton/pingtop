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
		false,
		true,
		nil,
		UpdateStatus{},
		0,
	)

	if !strings.Contains(output, "pingtop "+pingtop.Version) {
		t.Fatalf("expected versioned header, got %q", output)
	}
}

func TestBuildScreenWithSelectionMarksSelectedDisabledTarget(t *testing.T) {
	renderer := &Renderer{}
	errorMessage := "Request timed out while pinging the selected target"
	output := renderer.BuildScreenWithSelection(
		pingtop.StateSnapshot{
			Diagnosis:          "No targets enabled",
			StatsWindowSeconds: 3600,
			TargetStats: []pingtop.TargetStats{
				{
					Target:            "1.1.1.1",
					TargetType:        "ip",
					Disabled:          true,
					FailureCount:      4,
					PingFailureCount:  4,
					LastResult:        "PING_FAIL",
					LastResolvedIP:    "1.1.1.1",
					LastErrorCategory: "timeout",
					LastErrorMessage:  errorMessage,
				},
			},
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			StatsWindowSeconds:       3600,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
			Targets: []pingtop.TargetSpec{
				{Value: "1.1.1.1", Kind: "ip", Disabled: true},
			},
		},
		false,
		false,
		true,
		true,
		nil,
		UpdateStatus{},
		0,
		0,
	)

	if !strings.Contains(output, "> 1") || !strings.Contains(output, "disabled") || !strings.Contains(output, "targets 0/1") || !strings.Contains(output, "Focus") || !strings.Contains(output, "Details") {
		t.Fatalf("expected selected disabled target output, got %q", output)
	}
	targetRow := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "> 1") && strings.Contains(line, "1.1.1.1") {
			targetRow = line
			break
		}
	}
	if targetRow == "" {
		t.Fatalf("expected to find selected disabled target row, got %q", output)
	}
	if strings.Contains(targetRow, "timeout") || strings.Contains(targetRow, "Request timed out") {
		t.Fatalf("expected disabled table row to hide historical error details, got %q", targetRow)
	}
	if !strings.Contains(output, "timeout: "+errorMessage) {
		t.Fatalf("expected details panel to include full historical error, got %q", output)
	}
}

func TestBuildScreenHidesSelectedTargetDetailsWhenDisabled(t *testing.T) {
	renderer := &Renderer{}
	errorMessage := "Request timed out while pinging the selected target"
	output := renderer.BuildScreenWithSelection(
		pingtop.StateSnapshot{
			Diagnosis:          "Likely isolated target or path issue",
			StatsWindowSeconds: 3600,
			TargetStats: []pingtop.TargetStats{
				{
					Target:            "192.168.45.123",
					TargetType:        "ip",
					LastState:         "down",
					LastResult:        "PING_FAIL",
					LastCheckedAt:     time.Now(),
					FailureCount:      30,
					PingFailureCount:  30,
					LastResolvedIP:    "192.168.45.123",
					LastErrorCategory: "timeout",
					LastErrorMessage:  errorMessage,
				},
			},
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			StatsWindowSeconds:       3600,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
			Targets: []pingtop.TargetSpec{
				{Value: "192.168.45.123", Kind: "ip"},
			},
		},
		false,
		false,
		false,
		true,
		nil,
		UpdateStatus{},
		0,
		0,
	)

	if strings.Contains(output, "Details") {
		t.Fatalf("expected details panel to be hidden, got %q", output)
	}
	if strings.Contains(output, errorMessage) {
		t.Fatalf("expected full error text to stay out of the compact table, got %q", output)
	}
}

func TestBuildScreenKeepsPriorTargetStateWhileRefreshing(t *testing.T) {
	renderer := &Renderer{}
	latency := 22.0
	output := renderer.BuildScreenWithSelection(
		pingtop.StateSnapshot{
			Diagnosis:          "All monitored targets are reachable",
			StatsWindowSeconds: 3600,
			TargetStats: []pingtop.TargetStats{
				{
					Target:        "1.1.1.1",
					TargetType:    "ip",
					Checking:      true,
					LastState:     "up",
					LastResult:    "UP",
					LastCheckedAt: time.Now(),
					LastLatencyMS: &latency,
				},
			},
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			StatsWindowSeconds:       3600,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
			Targets: []pingtop.TargetSpec{
				{Value: "1.1.1.1", Kind: "ip"},
			},
		},
		false,
		false,
		false,
		true,
		nil,
		UpdateStatus{},
		0,
		0,
	)

	if strings.Contains(output, "checking") {
		t.Fatalf("expected prior target state to remain visible while refreshing, got %q", output)
	}
	row := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "1.1.1.1") {
			row = line
			break
		}
	}
	if !strings.Contains(row, " up") {
		t.Fatalf("expected target row to keep prior up state, got %q", row)
	}
}

func TestBuildScreenKeepsErrorColumnVisibleAtDemoWidth(t *testing.T) {
	originalTerminalSize := terminalSize
	terminalSize = func() (int, int) {
		return 120, 24
	}
	defer func() {
		terminalSize = originalTerminalSize
	}()

	renderer := &Renderer{}
	latency := 17.0
	output := renderer.BuildScreenWithSelection(
		pingtop.StateSnapshot{
			Diagnosis:          "Likely isolated target or path issue",
			StatsWindowSeconds: 3600,
			TargetStats: []pingtop.TargetStats{
				{
					Target:            "192.168.45.123",
					TargetType:        "ip",
					LastState:         "down",
					LastResult:        "PING_FAIL",
					LastCheckedAt:     time.Now(),
					LastLatencyMS:     &latency,
					FailureCount:      3,
					PingFailureCount:  3,
					LastResolvedIP:    "192.168.45.123",
					LastErrorCategory: "timeout",
					LastErrorMessage:  "Request timed out",
				},
			},
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			StatsWindowSeconds:       3600,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
			Targets: []pingtop.TargetSpec{
				{Value: "192.168.45.123", Kind: "ip"},
			},
		},
		false,
		false,
		false,
		true,
		nil,
		UpdateStatus{},
		0,
		0,
	)

	lines := strings.Split(output, "\n")
	header := ""
	row := ""
	for _, line := range lines {
		if strings.Contains(line, "Idx") && strings.Contains(line, "Target") {
			header = line
		}
		if strings.Contains(line, "192.168.45.123") && strings.Contains(line, "ping_fail") {
			row = line
		}
	}
	if header == "" || strings.Contains(header, "...") || !strings.Contains(header, "Error") {
		t.Fatalf("expected full table header with visible Error column, got %q in %q", header, output)
	}
	if row == "" || !strings.Contains(row, "timeout") {
		t.Fatalf("expected target row to keep compact error visible, got %q in %q", row, output)
	}
	if strings.Contains(header, "1h Loss%") || strings.Contains(header, "1h OK/Fail") {
		t.Fatalf("expected compact loss headers, got %q", header)
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

func TestBuildScreenPromptDoesNotCoverTargetRows(t *testing.T) {
	originalTerminalSize := terminalSize
	terminalSize = func() (int, int) {
		return 160, 28
	}
	defer func() {
		terminalSize = originalTerminalSize
	}()

	renderer := &Renderer{}
	targets := []pingtop.TargetSpec{
		{Value: "1.1.1.1", Kind: "ip"},
		{Value: "8.8.8.8", Kind: "ip"},
		{Value: "google.com", Kind: "hostname"},
		{Value: "cloudflare.com", Kind: "hostname"},
		{Value: "apple.com", Kind: "hostname"},
		{Value: "192.168.5.55", Kind: "ip", Disabled: true},
	}
	stats := make([]pingtop.TargetStats, 0, len(targets))
	for _, target := range targets {
		stats = append(stats, pingtop.TargetStats{
			Target:     target.Value,
			TargetType: target.Kind,
			Disabled:   target.Disabled,
			LastState:  "up",
			LastResult: "UP",
		})
	}

	output := renderer.BuildScreenWithSelection(
		pingtop.StateSnapshot{
			Diagnosis:          "All monitored targets are reachable",
			StatsWindowSeconds: 3600,
			TargetStats:        stats,
		},
		pingtop.AppConfig{
			CheckIntervalSeconds:     1,
			PingTimeoutMS:            1200,
			UIRefreshIntervalSeconds: 0.5,
			StatsWindowSeconds:       3600,
			LatencyWarningMS:         100,
			LatencyCriticalMS:        250,
			DiagnosisConfirmCycles:   2,
			RecoveryConfirmCycles:    2,
			LoggingMode:              "around_failure",
			VisibleEventLines:        8,
			Targets:                  targets,
		},
		false,
		true,
		false,
		true,
		&PromptState{Kind: "add", Message: "enter a hostname or IP to add"},
		UpdateStatus{},
		0,
		5,
	)

	rowIndex := strings.Index(output, "192.168.5.55")
	promptIndex := strings.Index(output, "Add Target")
	if rowIndex < 0 {
		t.Fatalf("expected selected sixth target row to remain visible, got %q", output)
	}
	if promptIndex < 0 {
		t.Fatalf("expected inline prompt panel, got %q", output)
	}
	if promptIndex < rowIndex {
		t.Fatalf("expected prompt to render below target rows, got %q", output)
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
	newest := renderer.BuildScreen(snapshot, config, false, false, false, true, nil, UpdateStatus{}, 0)
	older := renderer.BuildScreen(snapshot, config, false, false, false, true, nil, UpdateStatus{}, 2)

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

func TestBuildScreenCanHideEventsPane(t *testing.T) {
	originalTerminalSize := terminalSize
	terminalSize = func() (int, int) {
		return 80, 20
	}
	defer func() {
		terminalSize = originalTerminalSize
	}()

	renderer := &Renderer{}
	output := renderer.BuildScreen(
		pingtop.StateSnapshot{
			Diagnosis:          "Likely isolated target or path issue",
			StatsWindowSeconds: 3600,
			RecentEvents: []pingtop.EventEntry{
				{Timestamp: time.Unix(1, 0), Level: "warn", Message: "event should be hidden"},
			},
		},
		pingtop.AppConfig{
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
		},
		false,
		true,
		false,
		false,
		nil,
		UpdateStatus{},
		0,
	)

	if !strings.Contains(output, "events hidden") || !strings.Contains(output, "[e] show") {
		t.Fatalf("expected hidden events status and show shortcut, got %q", output)
	}
	if strings.Contains(output, "event should be hidden") || strings.Contains(output, "[PgUp/PgDn] page") {
		t.Fatalf("expected events pane content and paging shortcut to be hidden, got %q", output)
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
