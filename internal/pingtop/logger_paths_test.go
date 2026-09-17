package pingtop

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeLogResult(timestamp int64, pingSuccess bool, dnsSuccess *bool, errorCategory string) CheckResult {
	latency := 10.0
	latencyValue := &latency
	if !pingSuccess {
		latencyValue = nil
	}
	return CheckResult{
		CycleID:       1,
		Timestamp:     time.Unix(timestamp, 0),
		Target:        "1.1.1.1",
		TargetType:    "ip",
		ResolvedIP:    "1.1.1.1",
		DNSSuccess:    dnsSuccess,
		PingSuccess:   pingSuccess,
		LatencyMS:     latencyValue,
		ErrorCategory: errorCategory,
		ErrorMessage:  errorMessageForCategory(errorCategory),
	}
}

func TestResolveRuntimePathsUsesExecutableDirectoryAndGoRunFallback(t *testing.T) {
	tempDir := t.TempDir()
	execPaths := resolveRuntimePathsFor("./pingtop", tempDir, filepath.Join(tempDir, "pingtop"))
	if execPaths.RuntimeDir != tempDir {
		t.Fatalf("expected runtime dir %q, got %q", tempDir, execPaths.RuntimeDir)
	}

	goRunPaths := resolveRuntimePathsFor("./pingtop", tempDir, filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "pingtop"))
	if goRunPaths.RuntimeDir != tempDir {
		t.Fatalf("expected go-run runtime dir %q, got %q", tempDir, goRunPaths.RuntimeDir)
	}

	goRunExecutable := filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "pingtop")
	goRunPaths = resolveRuntimePathsFor(goRunExecutable, tempDir, goRunExecutable)
	if goRunPaths.RuntimeDir != tempDir {
		t.Fatalf("expected real go-run argv[0] to use runtime dir %q, got %q", tempDir, goRunPaths.RuntimeDir)
	}
}

func TestResolveRuntimePathsPreservesInstalledGoBuildDirectory(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"go-build-tools", "go-build123", "go-build"} {
		installed := filepath.Join(root, directory, "pingtop")
		paths := resolveRuntimePathsFor(installed, root, installed)
		if paths.RuntimeDir != filepath.Dir(installed) {
			t.Fatalf("installed binary %q must use its own directory, got %q", installed, paths.RuntimeDir)
		}
	}
}

func TestResolveRuntimePathsRecognizesGoRunLayouts(t *testing.T) {
	root := t.TempDir()
	for _, executable := range []string{
		filepath.Join(root, "custom-tmp", "go-build123", "b001", "exe", "pingtop"),
		filepath.Join(root, "custom-cache", "ab", strings.Repeat("ab", 32)+"-d", "pingtop"),
	} {
		paths := resolveRuntimePathsFor(executable, root, executable)
		if paths.RuntimeDir != root {
			t.Fatalf("go-run executable %q must use working directory, got %q", executable, paths.RuntimeDir)
		}
	}
}

func TestCSVLoggerAroundFailureCapturesBeforeAndAfter(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "pingtop_log.csv")
	logger := NewCSVLogger(path)
	config := defaultConfig()
	config.LoggingMode = "around_failure"
	config.AroundFailureBefore = 10
	config.AroundFailureAfter = 10

	logger.LogResults([]CheckResult{makeLogResult(100, true, nil, "ok"), makeLogResult(105, true, nil, "ok")}, config)
	logger.LogResults([]CheckResult{makeLogResult(109, false, nil, "timeout")}, config)
	logger.LogResults([]CheckResult{makeLogResult(115, true, nil, "ok")}, config)
	logger.LogResults([]CheckResult{makeLogResult(121, true, nil, "ok")}, config)

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open log: %v", err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("expected 5 csv rows including header, got %d", len(rows))
	}
	if rows[3][7] != "timeout" {
		t.Fatalf("expected timeout row, got %q", rows[3][7])
	}
}

func TestCSVLoggerAroundFailureUsesChronologicalBatchOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pingtop_log.csv")
	logger := NewCSVLogger(path)
	config := defaultConfig()
	config.AroundFailureBefore = 10
	config.AroundFailureAfter = 10
	if err := logger.LogResults([]CheckResult{makeLogResult(100, true, nil, "ok")}, config); err != nil {
		t.Fatal(err)
	}
	results := []CheckResult{
		makeLogResult(150, true, nil, "ok"),
		makeLogResult(105, false, nil, "timeout"),
		makeLogResult(110, true, nil, "ok"),
	}
	if err := logger.LogResults(results, config); err != nil {
		t.Fatal(err)
	}
	if results[0].Timestamp.Unix() != 150 {
		t.Fatal("logging mutated the caller's result order")
	}
	rows := readCSVLogRows(t, path)
	if len(rows) != 4 {
		t.Fatalf("expected pre-failure context, failure, and recovery; got %#v", rows)
	}
	for index, timestamp := range []int64{100, 105, 110} {
		if rows[index+1][0] != NowLocalISO(time.Unix(timestamp, 0), true) {
			t.Fatalf("unexpected context row at %d: %#v", index, rows[index+1])
		}
	}
}

func TestCSVLoggerOffDoesNotCreateLogFile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "pingtop_log.csv")
	logger := NewCSVLogger(path)
	config := defaultConfig()
	config.LoggingMode = LoggingModeOff

	logger.LogResults([]CheckResult{makeLogResult(100, false, nil, "timeout")}, config)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected logging off to avoid creating log file, got err=%v", err)
	}
}

func TestCSVLoggerChangesOnlyCapturesFailuresChangesAndRecovery(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "pingtop_log.csv")
	logger := NewCSVLogger(path)
	config := defaultConfig()
	config.LoggingMode = LoggingModeChangesOnly

	logger.LogResults([]CheckResult{makeLogResult(100, true, nil, "ok")}, config)
	logger.LogResults([]CheckResult{makeLogResult(101, false, nil, "timeout")}, config)
	logger.LogResults([]CheckResult{makeLogResult(102, false, nil, "timeout")}, config)
	logger.LogResults([]CheckResult{makeLogResult(103, false, nil, "ping_failure")}, config)
	logger.LogResults([]CheckResult{makeLogResult(104, true, nil, "ok")}, config)
	logger.LogResults([]CheckResult{makeLogResult(105, true, nil, "ok")}, config)

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open log: %v", err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected header plus first failure, changed failure, and recovery rows, got %d", len(rows))
	}
	if rows[1][7] != "timeout" || rows[2][7] != "ping_failure" || rows[3][7] != "ok" {
		t.Fatalf("unexpected change-only categories: %#v", []string{rows[1][7], rows[2][7], rows[3][7]})
	}
}

func TestRotationBySizeCreatesRotatedFile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "pingtop_log.csv")
	logger := NewCSVLogger(path)
	config := defaultConfig()
	config.LoggingMode = "all"
	config.LogRotationMaxMB = 1

	payload := make([]byte, 1024*1024+10)
	for index := range payload {
		payload[index] = 'x'
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("failed to prefill log: %v", err)
	}

	logger.LogResults([]CheckResult{makeLogResult(100, true, nil, "ok")}, config)
	matches, err := filepath.Glob(filepath.Join(tempDir, "pingtop_log_*.csv"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 rotated log, got %d", len(matches))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected active log to exist: %v", err)
	}
}

func TestCSVLoggerRetriesFailedWritesAcrossLoggingModes(t *testing.T) {
	for _, mode := range []string{LoggingModeAll, LoggingModeFailuresOnly, LoggingModeChangesOnly, LoggingModeAroundFailure} {
		t.Run(mode, func(t *testing.T) {
			// A file in place of the parent directory makes writes fail reliably,
			// including when tests run with elevated filesystem permissions.
			directory := filepath.Join(t.TempDir(), "logs")
			if err := os.WriteFile(directory, []byte("blocked"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "pingtop_log.csv")
			logger := NewCSVLogger(path)
			config := defaultConfig()
			config.LoggingMode = mode
			failure := makeLogResult(100, false, nil, "timeout")
			if err := logger.LogResults([]CheckResult{failure}, config); err == nil {
				t.Fatal("expected a write error to be reported")
			}
			if err := os.Remove(directory); err != nil {
				t.Fatal(err)
			}
			// Retry even when no new rows are selected for logging.
			if err := logger.LogResults(nil, config); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			if err := logger.LogResults(nil, config); err != nil {
				t.Fatalf("empty call failed: %v", err)
			}
			rows := readCSVLogRows(t, path)
			if len(rows) != 2 || rows[1][7] != "timeout" {
				t.Fatalf("expected the failed row exactly once after retry, got %#v", rows)
			}
		})
	}
}

func TestCSVLoggerRetryQueueIsBoundedAndReportsOverflow(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(directory, []byte("blocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger := NewCSVLogger(filepath.Join(directory, "pingtop_log.csv"))
	config := defaultConfig()
	config.LoggingMode = LoggingModeAll
	results := make([]CheckResult, maxPendingLogResults+1)
	for index := range results {
		results[index] = makeLogResult(int64(index), false, nil, "timeout")
	}
	err := logger.LogResults(results, config)
	if err == nil || !strings.Contains(err.Error(), "dropped 1 oldest unsaved rows") {
		t.Fatalf("expected explicit overflow warning, got %v", err)
	}
	if len(logger.pending) != maxPendingLogResults {
		t.Fatalf("retry queue grew past limit: %d", len(logger.pending))
	}
	if !logger.pending[0].Timestamp.Equal(results[1].Timestamp) {
		t.Fatal("expected retry queue to retain the most recent results")
	}
}

func TestCSVLoggerChangesOnlyRetriesWithoutLosingTransition(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(directory, []byte("blocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "pingtop_log.csv")
	logger := NewCSVLogger(path)
	config := defaultConfig()
	config.LoggingMode = LoggingModeChangesOnly
	if err := logger.LogResults([]CheckResult{makeLogResult(100, false, nil, "timeout")}, config); err == nil {
		t.Fatal("expected initial write failure")
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogResults([]CheckResult{makeLogResult(101, false, nil, "timeout")}, config); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogResults([]CheckResult{makeLogResult(102, true, nil, "ok")}, config); err != nil {
		t.Fatal(err)
	}
	rows := readCSVLogRows(t, path)
	if len(rows) != 3 || rows[1][7] != "timeout" || rows[2][7] != "ok" {
		t.Fatalf("expected the original failure and recovery, got %#v", rows)
	}
}

func readCSVLogRows(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
