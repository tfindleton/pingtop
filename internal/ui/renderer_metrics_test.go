package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

func recoveredTargetStats() TargetStats {
	stats := TargetStats{
		Target: "8.8.8.8", TargetType: "ip",
		TotalChecks: 9278, SuccessCount: 9277, FailureCount: 1, PingFailureCount: 1,
	}
	stats.Apply(pingtop.CheckResult{
		Timestamp:     time.Now().Add(-10 * time.Minute).Truncate(time.Second),
		ErrorCategory: "timeout", ErrorMessage: "Request timed out",
	})
	latency := 0.0
	stats.Apply(pingtop.CheckResult{
		Timestamp: time.Now(), PingSuccess: true, ErrorCategory: "ok", LatencyMS: &latency,
	})
	stats.WindowSummary = pingtop.CounterSummary{Checks: 6911, Successes: 6910, Failures: 1, PingFailures: 1}
	return stats
}

func TestRecoveredTargetDisplaysSmallLossAndLastFailure(t *testing.T) {
	renderer := &Renderer{}
	config := AppConfig{StatsWindowSeconds: 3600, LoggingMode: pingtop.LoggingModeOff}
	stats := recoveredTargetStats()
	lines := renderer.buildTargetTable([]TargetStats{stats}, 120, config, false, -1)
	fields := strings.Fields(lines[2])
	if len(fields) < 9 || fields[4] != "<1ms" || fields[5] != "2" || fields[7] != "<0.1%" || fields[8] != "6.91k/1" {
		t.Fatalf("small measurements or session failures were hidden: %q", lines[2])
	}
	for _, line := range lines {
		if len(line) > 120 {
			t.Fatalf("table overflowed its width: %q", line)
		}
	}

	details := strings.Join(renderer.buildTargetDetailsBlock([]TargetStats{stats}, config, 0, 80), "\n")
	for _, expected := range []string{
		"loss <0.1%", "latency <1ms", "current none", "Failure",
		stats.LastFailureAt.Local().Format("2006-01-02 15:04:05"), "timeout: Request timed out",
	} {
		if !strings.Contains(details, expected) {
			t.Fatalf("missing %q in recovered target details:\n%s", expected, details)
		}
	}
	for _, line := range strings.Split(details, "\n") {
		if len(line) > 80 {
			t.Fatalf("details overflowed their width: %q", line)
		}
	}

	stats.ResetCounters()
	details = strings.Join(renderer.buildTargetDetailsBlock([]TargetStats{stats}, config, 0, 80), "\n")
	if !strings.Contains(details, "last none since reset") || strings.Contains(details, "Request timed out") {
		t.Fatalf("last failure should clear with session counters:\n%s", details)
	}
}

func TestReportRetainsFailureHistoryWithLoggingOff(t *testing.T) {
	renderer := &Renderer{ansi: true}
	stats := recoveredTargetStats()
	report := renderer.BuildReport(StateSnapshot{TargetStats: []TargetStats{stats}}, AppConfig{
		StatsWindowSeconds: 3600, LoggingMode: pingtop.LoggingModeOff,
	}, false)
	for _, expected := range []string{
		"csv_logging: off (not saved)", "Last failures since counter reset", "8.8.8.8 at",
		pingtop.NowLocalISO(stats.LastFailureAt, false), "timeout: Request timed out", "<1ms", "<0.1%",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("snapshot lost %q after recovery with logging off:\n%s", expected, report)
		}
	}
	if strings.Contains(report, "\x1b") {
		t.Fatal("saved report contains terminal color escapes")
	}
	if !renderer.ansi {
		t.Fatal("saving a report disabled terminal colors")
	}
}
