package pingtop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferTargetNormalizesIPAndHostname(t *testing.T) {
	ipTarget, err := InferTarget(" 1.1.1.1 ")
	if err != nil {
		t.Fatalf("inferTarget returned error: %v", err)
	}
	if ipTarget.Kind != "ip" {
		t.Fatalf("expected ip kind, got %q", ipTarget.Kind)
	}

	hostTarget, err := InferTarget("Cloudflare.com.")
	if err != nil {
		t.Fatalf("inferTarget returned error: %v", err)
	}
	if hostTarget.Value != "cloudflare.com" {
		t.Fatalf("expected normalized hostname, got %q", hostTarget.Value)
	}
}

func TestInferTargetRejectsInvalidValues(t *testing.T) {
	if _, err := InferTarget(" "); err == nil {
		t.Fatal("expected empty target error")
	}
	if _, err := InferTarget("bad host name"); err == nil {
		t.Fatal("expected whitespace hostname error")
	}
}

func TestConfigNormalizesAndDeduplicatesTargets(t *testing.T) {
	config := configFromMap(map[string]any{
		"check_interval_seconds":        0.1,
		"ping_timeout_ms":               999999,
		"ui_refresh_interval_seconds":   10.0,
		"help_visible":                  false,
		"stats_window_seconds":          5,
		"diagnosis_confirm_cycles":      0,
		"recovery_confirm_cycles":       99,
		"latency_warning_ms":            250,
		"latency_critical_ms":           100,
		"logging_mode":                  "nope",
		"around_failure_before_seconds": -5,
		"around_failure_after_seconds":  9999,
		"log_rotation_max_mb":           -1,
		"log_rotation_keep_files":       0,
		"event_history_size":            999,
		"visible_event_lines":           1,
		"targets": []any{
			"8.8.8.8",
			"8.8.8.8",
			"Google.com.",
		},
	})
	config.Normalize()

	if config.CheckIntervalSeconds != 0.5 {
		t.Fatalf("unexpected check interval: %v", config.CheckIntervalSeconds)
	}
	if config.HelpVisible {
		t.Fatal("expected help visibility override to be preserved")
	}
	if config.PingTimeoutMS != 30000 {
		t.Fatalf("unexpected ping timeout: %d", config.PingTimeoutMS)
	}
	if config.UIRefreshIntervalSeconds != 5.0 {
		t.Fatalf("unexpected refresh interval: %v", config.UIRefreshIntervalSeconds)
	}
	if config.StatsWindowSeconds != 30 {
		t.Fatalf("unexpected stats window: %d", config.StatsWindowSeconds)
	}
	if config.DiagnosisConfirmCycles != 1 {
		t.Fatalf("unexpected diagnosis cycles: %d", config.DiagnosisConfirmCycles)
	}
	if config.RecoveryConfirmCycles != 10 {
		t.Fatalf("unexpected recovery cycles: %d", config.RecoveryConfirmCycles)
	}
	if config.LatencyCriticalMS != 250 {
		t.Fatalf("unexpected critical latency: %d", config.LatencyCriticalMS)
	}
	if config.LoggingMode != "around_failure" {
		t.Fatalf("unexpected logging mode: %q", config.LoggingMode)
	}
	if config.AroundFailureBefore != 0 || config.AroundFailureAfter != 600 {
		t.Fatalf("unexpected around-failure window: %d/%d", config.AroundFailureBefore, config.AroundFailureAfter)
	}
	if config.LogRotationMaxMB != 0 || config.LogRotationKeepFiles != 1 {
		t.Fatalf("unexpected rotation settings: %d/%d", config.LogRotationMaxMB, config.LogRotationKeepFiles)
	}
	if config.EventHistorySize != 200 || config.VisibleEventLines != 3 {
		t.Fatalf("unexpected event settings: %d/%d", config.EventHistorySize, config.VisibleEventLines)
	}
	if len(config.Targets) != 2 || config.Targets[0].Value != "8.8.8.8" || config.Targets[1].Value != "google.com" {
		t.Fatalf("unexpected targets: %#v", config.Targets)
	}
}

func TestConfigManagerWritesDefaultsAndRecoversFromBadJSON(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "pingtop.json")

	manager := NewConfigManager(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file to exist: %v", err)
	}
	if first := manager.Snapshot().Targets[0].Value; first != "1.1.1.1" {
		t.Fatalf("unexpected first default target: %q", first)
	}
	if got := manager.Snapshot().UIRefreshIntervalSeconds; got != 1.0 {
		t.Fatalf("unexpected default UI refresh interval: %.2f", got)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("failed to write bad json: %v", err)
	}
	manager = NewConfigManager(path)
	if warning := manager.LoadWarning(); !strings.Contains(warning, "Invalid config") {
		t.Fatalf("expected invalid config warning, got %q", warning)
	}
}

func TestDefaultConfigUsesEmbeddedUpdateRepoURL(t *testing.T) {
	original := DefaultUpdateRepoURL
	DefaultUpdateRepoURL = "https://github.com/example/pingtop"
	defer func() {
		DefaultUpdateRepoURL = original
	}()

	config := defaultConfig()
	if config.UpdateRepoURL != DefaultUpdateRepoURL {
		t.Fatalf("expected embedded update repo url %q, got %q", DefaultUpdateRepoURL, config.UpdateRepoURL)
	}
	if !config.HelpVisible {
		t.Fatal("expected help to be visible by default")
	}
}

func TestConfigNormalizeMigratesLegacyUpdateRepoURL(t *testing.T) {
	for _, legacyURL := range []string{
		"https://github.com/Landmine-1252/pingtop",
		"https://github.com/Landmine-1252/pingtop-go",
		"https://github.com/Landmine-1252/pingtop-go.git",
	} {
		config := configFromMap(map[string]any{
			"update_repo_url": legacyURL,
			"targets":         []any{"1.1.1.1"},
		})
		config.Normalize()

		if config.UpdateRepoURL != DefaultUpdateRepoURL {
			t.Fatalf("expected legacy repo url %q to migrate to %q, got %q", legacyURL, DefaultUpdateRepoURL, config.UpdateRepoURL)
		}
	}
}

func TestConfigManagerLoadsLegacyUpdateRepoURLAsCurrentRepo(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "pingtop.json")
	if err := os.WriteFile(path, []byte(`{
  "version": 1,
  "update_repo_url": "https://github.com/Landmine-1252/pingtop-go",
  "targets": [{"value":"1.1.1.1","type":"ip"}]
}`), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	manager := NewConfigManager(path)
	if got := manager.Snapshot().UpdateRepoURL; got != DefaultUpdateRepoURL {
		t.Fatalf("expected migrated repo url %q, got %q", DefaultUpdateRepoURL, got)
	}
}

func TestConfigFromMapDefaultsHelpVisibleForOlderConfigs(t *testing.T) {
	config := configFromMap(map[string]any{
		"targets": []any{"1.1.1.1"},
	})
	if !config.HelpVisible {
		t.Fatal("expected help to default to visible when field is missing")
	}
}

func TestConfigManagerRevisionChangesOnUpdateAndSave(t *testing.T) {
	tempDir := t.TempDir()
	manager := NewConfigManager(filepath.Join(tempDir, "pingtop.json"))
	initial := manager.Revision()

	manager.Update(func(config *AppConfig) {
		config.CheckIntervalSeconds = 2.0
	})
	if got := manager.Revision(); got <= initial {
		t.Fatalf("expected revision to increase after update: initial=%d got=%d", initial, got)
	}

	afterUpdate := manager.Revision()
	manager.Save()
	if got := manager.Revision(); got <= afterUpdate {
		t.Fatalf("expected revision to increase after save: afterUpdate=%d got=%d", afterUpdate, got)
	}
}
