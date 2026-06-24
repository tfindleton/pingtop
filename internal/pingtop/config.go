package pingtop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var defaultTargetValues = []string{
	"1.1.1.1",
	"8.8.8.8",
	"google.com",
	"cloudflare.com",
	"apple.com",
}

const (
	LoggingModeAll           = "all"
	LoggingModeFailuresOnly  = "failures_only"
	LoggingModeChangesOnly   = "changes_only"
	LoggingModeAroundFailure = "around_failure"
	LoggingModeOff           = "off"
)

const (
	CheckIntervalMinSeconds     = 0.5
	CheckIntervalMaxSeconds     = 300.0
	UIRefreshIntervalMinSeconds = 0.1
	UIRefreshIntervalMaxSeconds = 5.0
)

var loggingModes = []string{
	LoggingModeAroundFailure,
	LoggingModeChangesOnly,
	LoggingModeFailuresOnly,
	LoggingModeAll,
	LoggingModeOff,
}

var legacyDefaultUpdateRepoURLs = []string{
	"https://github.com/Landmine-1252/pingtop",
	"https://github.com/Landmine-1252/pingtop-go",
}

type AppConfig struct {
	Version                  int          `json:"version"`
	CheckIntervalSeconds     float64      `json:"check_interval_seconds"`
	PingTimeoutMS            int          `json:"ping_timeout_ms"`
	UIRefreshIntervalSeconds float64      `json:"ui_refresh_interval_seconds"`
	HelpVisible              bool         `json:"help_visible"`
	DetailsVisible           bool         `json:"details_visible"`
	EventsVisible            bool         `json:"events_visible"`
	StatsWindowSeconds       int          `json:"stats_window_seconds"`
	UpdateCheckEnabled       bool         `json:"update_check_enabled"`
	UpdateRepoURL            string       `json:"update_repo_url"`
	DiagnosisConfirmCycles   int          `json:"diagnosis_confirm_cycles"`
	RecoveryConfirmCycles    int          `json:"recovery_confirm_cycles"`
	LatencyWarningMS         int          `json:"latency_warning_ms"`
	LatencyCriticalMS        int          `json:"latency_critical_ms"`
	LoggingMode              string       `json:"logging_mode"`
	AroundFailureBefore      int          `json:"around_failure_before_seconds"`
	AroundFailureAfter       int          `json:"around_failure_after_seconds"`
	LogRotationMaxMB         int          `json:"log_rotation_max_mb"`
	LogRotationKeepFiles     int          `json:"log_rotation_keep_files"`
	EventHistorySize         int          `json:"event_history_size"`
	VisibleEventLines        int          `json:"visible_event_lines"`
	Targets                  []TargetSpec `json:"targets"`
}

func defaultConfig() AppConfig {
	targets := make([]TargetSpec, 0, len(defaultTargetValues))
	for _, value := range defaultTargetValues {
		target, err := InferTarget(value)
		if err == nil {
			targets = append(targets, target)
		}
	}
	return AppConfig{
		Version:                  1,
		CheckIntervalSeconds:     1.0,
		PingTimeoutMS:            1200,
		UIRefreshIntervalSeconds: 1.0,
		HelpVisible:              true,
		DetailsVisible:           false,
		EventsVisible:            true,
		StatsWindowSeconds:       3600,
		UpdateCheckEnabled:       true,
		UpdateRepoURL:            DefaultUpdateRepoURL,
		DiagnosisConfirmCycles:   2,
		RecoveryConfirmCycles:    2,
		LatencyWarningMS:         100,
		LatencyCriticalMS:        250,
		LoggingMode:              LoggingModeAroundFailure,
		AroundFailureBefore:      15,
		AroundFailureAfter:       15,
		LogRotationMaxMB:         25,
		LogRotationKeepFiles:     10,
		EventHistorySize:         40,
		VisibleEventLines:        8,
		Targets:                  targets,
	}
}

func (config AppConfig) Clone() AppConfig {
	clone := config
	clone.Targets = append([]TargetSpec(nil), config.Targets...)
	return clone
}

func (config AppConfig) EnabledTargets() []TargetSpec {
	targets := make([]TargetSpec, 0, len(config.Targets))
	for _, target := range config.Targets {
		if target.Disabled {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}

func (config AppConfig) EnabledTargetCount() int {
	count := 0
	for _, target := range config.Targets {
		if !target.Disabled {
			count++
		}
	}
	return count
}

func (config *AppConfig) Normalize() {
	config.Version = 1
	config.CheckIntervalSeconds = mathRound(clampFloat(config.CheckIntervalSeconds, CheckIntervalMinSeconds, CheckIntervalMaxSeconds), 2)
	config.PingTimeoutMS = int(clampFloat(float64(config.PingTimeoutMS), 250.0, 30000.0))
	config.UIRefreshIntervalSeconds = mathRound(clampFloat(config.UIRefreshIntervalSeconds, UIRefreshIntervalMinSeconds, UIRefreshIntervalMaxSeconds), 2)
	config.StatsWindowSeconds = int(clampFloat(float64(config.StatsWindowSeconds), 30.0, 2_592_000.0))
	config.UpdateRepoURL = normalizeUpdateRepoURL(config.UpdateRepoURL)
	config.DiagnosisConfirmCycles = int(clampFloat(float64(config.DiagnosisConfirmCycles), 1.0, 10.0))
	config.RecoveryConfirmCycles = int(clampFloat(float64(config.RecoveryConfirmCycles), 1.0, 10.0))
	config.LatencyWarningMS = int(clampFloat(float64(config.LatencyWarningMS), 10.0, 10000.0))
	config.LatencyCriticalMS = int(clampFloat(float64(config.LatencyCriticalMS), 10.0, 10000.0))
	if config.LatencyCriticalMS < config.LatencyWarningMS {
		config.LatencyCriticalMS = config.LatencyWarningMS
	}
	if !containsString(loggingModes, config.LoggingMode) {
		config.LoggingMode = LoggingModeAroundFailure
	}
	config.AroundFailureBefore = int(clampFloat(float64(config.AroundFailureBefore), 0.0, 600.0))
	config.AroundFailureAfter = int(clampFloat(float64(config.AroundFailureAfter), 0.0, 600.0))
	config.LogRotationMaxMB = int(clampFloat(float64(config.LogRotationMaxMB), 0.0, 1024.0))
	config.LogRotationKeepFiles = int(clampFloat(float64(config.LogRotationKeepFiles), 1.0, 100.0))
	config.EventHistorySize = int(clampFloat(float64(config.EventHistorySize), 10.0, 200.0))
	config.VisibleEventLines = int(clampFloat(float64(config.VisibleEventLines), 3.0, 20.0))

	normalizedTargets := make([]TargetSpec, 0, len(config.Targets))
	seen := make(map[string]struct{})
	for _, target := range config.Targets {
		normalized, err := InferTarget(target.Value)
		if err != nil {
			continue
		}
		normalized.Disabled = target.Disabled
		key := normalized.Kind + ":" + normalized.Value
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalizedTargets = append(normalizedTargets, normalized)
	}
	config.Targets = normalizedTargets
}

func normalizeUpdateRepoURL(raw string) string {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "git@github.com:") {
		value = "https://github.com/" + strings.TrimPrefix(value, "git@github.com:")
	}
	if strings.HasSuffix(value, ".git") {
		value = strings.TrimSuffix(value, ".git")
	}
	value = strings.TrimRight(value, "/")
	for _, legacyURL := range legacyDefaultUpdateRepoURLs {
		if strings.EqualFold(value, legacyURL) {
			return DefaultUpdateRepoURL
		}
	}
	return value
}

type ConfigManager struct {
	path        string
	mu          sync.RWMutex
	loadWarning string
	config      AppConfig
	revision    uint64
}

func NewConfigManager(path string) *ConfigManager {
	manager := &ConfigManager{path: path}
	manager.config = manager.load()
	manager.revision = 1
	return manager
}

func NewTransientConfigManager(config AppConfig) *ConfigManager {
	config.Normalize()
	return &ConfigManager{
		config:   config,
		revision: 1,
	}
}

func (manager *ConfigManager) LoadWarning() string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.loadWarning
}

func (manager *ConfigManager) Snapshot() AppConfig {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.config.Clone()
}

func (manager *ConfigManager) Revision() uint64 {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.revision
}

func (manager *ConfigManager) Save() AppConfig {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.config.Normalize()
	_ = manager.write(manager.config)
	manager.revision++
	return manager.config.Clone()
}

func (manager *ConfigManager) Update(updater func(*AppConfig)) AppConfig {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	updater(&manager.config)
	manager.config.Normalize()
	_ = manager.write(manager.config)
	manager.revision++
	return manager.config.Clone()
}

func (manager *ConfigManager) load() AppConfig {
	if _, err := os.Stat(manager.path); os.IsNotExist(err) {
		config := defaultConfig()
		config.Normalize()
		_ = manager.write(config)
		return config
	}

	data, err := os.ReadFile(manager.path)
	if err != nil {
		manager.loadWarning = fmt.Sprintf("Invalid config; using defaults in memory (%v)", err)
		config := defaultConfig()
		config.Normalize()
		return config
	}

	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		manager.loadWarning = fmt.Sprintf("Invalid config; using defaults in memory (%v)", err)
		config := defaultConfig()
		config.Normalize()
		return config
	}

	parsed, ok := raw.(map[string]any)
	if !ok {
		config := defaultConfig()
		config.Normalize()
		return config
	}

	config := configFromMap(parsed)
	config.Normalize()
	return config
}

func LoggingModes() []string {
	return append([]string(nil), loggingModes...)
}

func (manager *ConfigManager) write(config AppConfig) error {
	if manager.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(manager.path), 0o755); err != nil {
		return err
	}
	payload, err := marshalConfig(config)
	if err != nil {
		return err
	}
	return os.WriteFile(manager.path, payload, 0o644)
}

func marshalConfig(config AppConfig) ([]byte, error) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func configFromMap(data map[string]any) AppConfig {
	base := defaultConfig()
	targets := parseTargets(data["targets"])
	if len(targets) == 0 {
		targets = base.Targets
	}
	return AppConfig{
		Version:                  asInt(data["version"], base.Version),
		CheckIntervalSeconds:     asFloat(data["check_interval_seconds"], base.CheckIntervalSeconds),
		PingTimeoutMS:            asInt(data["ping_timeout_ms"], base.PingTimeoutMS),
		UIRefreshIntervalSeconds: asFloat(data["ui_refresh_interval_seconds"], base.UIRefreshIntervalSeconds),
		HelpVisible:              asBool(data["help_visible"], base.HelpVisible),
		DetailsVisible:           asBool(data["details_visible"], base.DetailsVisible),
		EventsVisible:            asBool(data["events_visible"], base.EventsVisible),
		StatsWindowSeconds:       asInt(data["stats_window_seconds"], base.StatsWindowSeconds),
		UpdateCheckEnabled:       asBool(data["update_check_enabled"], base.UpdateCheckEnabled),
		UpdateRepoURL:            asString(data["update_repo_url"], base.UpdateRepoURL),
		DiagnosisConfirmCycles:   asInt(data["diagnosis_confirm_cycles"], base.DiagnosisConfirmCycles),
		RecoveryConfirmCycles:    asInt(data["recovery_confirm_cycles"], base.RecoveryConfirmCycles),
		LatencyWarningMS:         asInt(data["latency_warning_ms"], base.LatencyWarningMS),
		LatencyCriticalMS:        asInt(data["latency_critical_ms"], base.LatencyCriticalMS),
		LoggingMode:              asString(data["logging_mode"], base.LoggingMode),
		AroundFailureBefore:      asInt(data["around_failure_before_seconds"], base.AroundFailureBefore),
		AroundFailureAfter:       asInt(data["around_failure_after_seconds"], base.AroundFailureAfter),
		LogRotationMaxMB:         asInt(data["log_rotation_max_mb"], base.LogRotationMaxMB),
		LogRotationKeepFiles:     asInt(data["log_rotation_keep_files"], base.LogRotationKeepFiles),
		EventHistorySize:         asInt(data["event_history_size"], base.EventHistorySize),
		VisibleEventLines:        asInt(data["visible_event_lines"], base.VisibleEventLines),
		Targets:                  targets,
	}
}

func parseTargets(value any) []TargetSpec {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	targets := make([]TargetSpec, 0, len(items))
	for _, item := range items {
		target, err := parseTargetSpec(item)
		if err == nil {
			targets = append(targets, target)
		}
	}
	return targets
}

func parseTargetSpec(value any) (TargetSpec, error) {
	switch item := value.(type) {
	case string:
		return InferTarget(item)
	case map[string]any:
		rawValue := asString(item["value"], "")
		rawKind := strings.ToLower(asString(item["type"], ""))
		target, err := InferTarget(rawValue)
		if err != nil {
			return TargetSpec{}, err
		}
		if rawKind != "" && rawKind != target.Kind {
			return TargetSpec{}, fmt.Errorf("target type mismatch for %s", rawValue)
		}
		target.Disabled = asBool(item["disabled"], false)
		if _, exists := item["enabled"]; exists {
			target.Disabled = !asBool(item["enabled"], true)
		}
		return target, nil
	default:
		return TargetSpec{}, errors.New("target entry must be a string or object")
	}
}

func asString(value any, fallback string) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fallback
	}
}

func asInt(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

func asFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func asBool(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(typed))
		if trimmed == "true" {
			return true
		}
		if trimmed == "false" {
			return false
		}
	}
	return fallback
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mathRound(value float64, places int) float64 {
	pow := mathPow10(places)
	return float64(int(value*pow+0.5)) / pow
}

func mathPow10(power int) float64 {
	value := 1.0
	for i := 0; i < power; i++ {
		value *= 10
	}
	return value
}
