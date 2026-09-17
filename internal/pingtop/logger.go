package pingtop

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxPendingLogResults = 10000

var csvFieldNames = []string{
	"timestamp",
	"target",
	"target_type",
	"resolved_ip",
	"dns_success",
	"ping_success",
	"latency_ms",
	"error_category",
	"error_message",
	"worker_id",
	"cycle_id",
	"sequence",
}

type CSVLogger struct {
	path         string
	mu           sync.Mutex
	buffer       []BufferedLogResult
	captureUntil time.Time
	currentMode  string
	lastChanges  map[string]string
	pending      []CheckResult
}

func NewCSVLogger(path string) *CSVLogger {
	return &CSVLogger{
		path:   path,
		buffer: make([]BufferedLogResult, 0, 128),
	}
}

func NewDisabledCSVLogger() *CSVLogger {
	return &CSVLogger{
		buffer: make([]BufferedLogResult, 0, 128),
	}
}

func (logger *CSVLogger) ensureHeader() error {
	if info, err := os.Stat(logger.path); err == nil && info.Size() > 0 {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logger.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(logger.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(file)
	if err := writer.Write(csvFieldNames); err != nil {
		return errors.Join(err, file.Truncate(0), file.Close())
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return errors.Join(err, file.Truncate(0), file.Close())
	}
	return file.Close()
}

func (logger *CSVLogger) LogResults(results []CheckResult, config AppConfig) error {
	if logger.path == "" {
		return nil
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()

	if config.LoggingMode == LoggingModeOff {
		logger.buffer = logger.buffer[:0]
		logger.captureUntil = time.Time{}
		logger.currentMode = config.LoggingMode
		logger.lastChanges = nil
		clear(logger.pending)
		logger.pending = nil
		return nil
	}
	if logger.currentMode != config.LoggingMode {
		logger.buffer = logger.buffer[:0]
		logger.captureUntil = time.Time{}
		logger.lastChanges = nil
		logger.currentMode = config.LoggingMode
	}

	if config.LoggingMode == LoggingModeAroundFailure && len(results) > 1 {
		// Concurrent checks are returned in target order. Process their completion
		// times in order so a later success cannot prune an earlier failure's context.
		results = append([]CheckResult(nil), results...)
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].Timestamp.Before(results[j].Timestamp)
		})
	}

	rows := make([]CheckResult, 0, len(results))
	for _, result := range results {
		switch config.LoggingMode {
		case LoggingModeAll:
			rows = append(rows, result)
		case LoggingModeFailuresOnly:
			if result.IsFailure() {
				rows = append(rows, result)
			}
		case LoggingModeChangesOnly:
			if logger.isChangedResult(result) {
				rows = append(rows, result)
			}
		default:
			rows = append(rows, logger.logAroundFailure(result, config)...)
		}
	}
	logger.pending = append(logger.pending, rows...)
	if err := logger.writeRows(logger.pending, config); err != nil {
		if dropped := len(logger.pending) - maxPendingLogResults; dropped > 0 {
			copy(logger.pending, logger.pending[dropped:])
			clear(logger.pending[maxPendingLogResults:])
			logger.pending = logger.pending[:maxPendingLogResults]
			return fmt.Errorf("CSV log write failed: %w; dropped %d oldest unsaved rows (retry limit %d)", err, dropped, maxPendingLogResults)
		}
		return fmt.Errorf("CSV log write failed: %w", err)
	}
	clear(logger.pending)
	logger.pending = logger.pending[:0]
	return nil
}

func (logger *CSVLogger) logAroundFailure(result CheckResult, config AppConfig) []CheckResult {
	record := BufferedLogResult{Result: result}
	logger.buffer = append(logger.buffer, record)
	logger.pruneBuffer(result.Timestamp, config.AroundFailureBefore)
	rows := make([]CheckResult, 0, 4)

	if result.IsFailure() {
		candidate := result.Timestamp.Add(time.Duration(config.AroundFailureAfter) * time.Second)
		if candidate.After(logger.captureUntil) {
			logger.captureUntil = candidate
		}
		rows = append(rows, logger.flushBuffer()...)
	}

	if !logger.captureUntil.IsZero() && !result.Timestamp.After(logger.captureUntil) {
		last := &logger.buffer[len(logger.buffer)-1]
		if !last.Written {
			rows = append(rows, result)
			last.Written = true
		}
	} else if !logger.captureUntil.IsZero() && result.Timestamp.After(logger.captureUntil) {
		logger.captureUntil = time.Time{}
	}
	return rows
}

func (logger *CSVLogger) isChangedResult(result CheckResult) bool {
	if logger.lastChanges == nil {
		logger.lastChanges = make(map[string]string)
	}
	key := result.TargetType + ":" + result.Target
	signature := changeSignature(result)
	previous, known := logger.lastChanges[key]
	logger.lastChanges[key] = signature

	if !known {
		return result.IsFailure()
	}
	if previous == signature {
		return false
	}
	return result.IsFailure() || previous != "ok"
}

func changeSignature(result CheckResult) string {
	if !result.IsFailure() {
		return "ok"
	}
	return result.StatusText() + ":" + result.ErrorCategory
}

func (logger *CSVLogger) pruneBuffer(current time.Time, beforeSeconds int) {
	cutoff := current.Add(-time.Duration(beforeSeconds) * time.Second)
	index := 0
	for index < len(logger.buffer) && logger.buffer[index].Result.Timestamp.Before(cutoff) {
		index++
	}
	if index > 0 {
		copy(logger.buffer, logger.buffer[index:])
		clear(logger.buffer[len(logger.buffer)-index:])
		logger.buffer = logger.buffer[:len(logger.buffer)-index]
	}
}

func (logger *CSVLogger) flushBuffer() []CheckResult {
	rows := make([]CheckResult, 0)
	for index := range logger.buffer {
		if logger.buffer[index].Written {
			continue
		}
		rows = append(rows, logger.buffer[index].Result)
		logger.buffer[index].Written = true
	}
	return rows
}

func (logger *CSVLogger) writeRows(results []CheckResult, config AppConfig) error {
	if len(results) == 0 {
		return nil
	}
	if err := logger.rotateIfNeeded(config); err != nil {
		return err
	}
	if err := logger.ensureHeader(); err != nil {
		return err
	}

	var payload bytes.Buffer
	writer := csv.NewWriter(&payload)
	for _, result := range results {
		record := []string{
			NowLocalISO(result.Timestamp, true),
			result.Target,
			result.TargetType,
			result.ResolvedIP,
			formatOptionalBool(result.DNSSuccess),
			boolString(result.PingSuccess),
			formatOptionalFloat(result.LatencyMS),
			result.ErrorCategory,
			result.ErrorMessage,
			result.WorkerID,
			fmt.Sprintf("%d", result.CycleID),
			fmt.Sprintf("%d", result.Sequence),
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}

	file, err := os.OpenFile(logger.path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload.Bytes()); err != nil {
		// Retrying after a partial write can repeat rows, but truncating would risk
		// removing rows concurrently appended by another pingtop process.
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func (logger *CSVLogger) rotateIfNeeded(config AppConfig) error {
	maxBytes := int64(config.LogRotationMaxMB) * 1024 * 1024
	if maxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(logger.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < maxBytes {
		return nil
	}

	timestamp := time.Now().Format("20060102_150405")
	rotatedPath := strings.TrimSuffix(logger.path, filepath.Ext(logger.path)) + "_" + timestamp + filepath.Ext(logger.path)
	suffix := 1
	for {
		if _, err := os.Stat(rotatedPath); os.IsNotExist(err) {
			break
		} else if err != nil {
			return err
		}
		rotatedPath = strings.TrimSuffix(logger.path, filepath.Ext(logger.path)) + "_" + timestamp + fmt.Sprintf("_%d", suffix) + filepath.Ext(logger.path)
		suffix++
	}

	if err := os.Rename(logger.path, rotatedPath); err != nil {
		return err
	}
	if err := logger.ensureHeader(); err != nil {
		return err
	}
	logger.cleanupRotatedLogs(config.LogRotationKeepFiles)
	return nil
}

func (logger *CSVLogger) cleanupRotatedLogs(keepFiles int) {
	pattern := strings.TrimSuffix(logger.path, filepath.Ext(logger.path)) + "_*" + filepath.Ext(logger.path)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	sort.Slice(matches, func(i, j int) bool {
		infoI, errI := os.Stat(matches[i])
		infoJ, errJ := os.Stat(matches[j])
		if errI != nil || errJ != nil {
			return matches[i] > matches[j]
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})
	if keepFiles >= len(matches) {
		return
	}
	for _, path := range matches[keepFiles:] {
		_ = os.Remove(path)
	}
}

func formatOptionalBool(value *bool) string {
	if value == nil {
		return ""
	}
	return boolString(*value)
}

func formatOptionalFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *value)
}
