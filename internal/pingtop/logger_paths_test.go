package pingtop

import (
	"encoding/csv"
	"os"
	"path/filepath"
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
