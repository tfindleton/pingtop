package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

func TestUILoggingDisplayUsesActualCSVAvailability(t *testing.T) {
	t.Setenv("COLUMNS", "140")
	t.Setenv("LINES", "42")
	for _, test := range []struct {
		name       string
		adHoc      bool
		mode       string
		csvState   string
		label      string
		enableHint bool
	}{
		{name: "normal off", mode: pingtop.LoggingModeOff, csvState: "off", label: "off (no CSV)", enableHint: true},
		{name: "normal enabled", mode: pingtop.LoggingModeAll, csvState: "enabled", label: "all"},
		{name: "ad hoc off", adHoc: true, mode: pingtop.LoggingModeOff, csvState: "disabled", label: "off (CSV disabled; ad hoc)"},
		{name: "ad hoc enabled mode", adHoc: true, mode: pingtop.LoggingModeAll, csvState: "disabled", label: "all (CSV disabled; ad hoc)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			args := cliArgs{}
			if test.adHoc {
				args.targets = []string{"192.0.2.1"}
			}
			services, err := buildServices(RuntimePaths{
				RuntimeDir: directory,
				ConfigPath: filepath.Join(directory, "pingtop.json"),
				LogPath:    filepath.Join(directory, "pingtop_log.csv"),
			}, args)
			if err != nil {
				t.Fatal(err)
			}
			defer services.coordinator.Close()
			config := services.configManager.Update(func(config *AppConfig) {
				config.LoggingMode = test.mode
			})
			if services.logger.Enabled() == test.adHoc {
				t.Fatalf("unexpected CSV availability for adHoc=%v", test.adHoc)
			}
			ui := NewPingTopUI(services.runtimePaths, services.configManager, services.stateStore,
				services.logger, services.coordinator, services.updateManager)
			snapshot := services.stateStore.Snapshot()
			for _, helpVisible := range []bool{true, false} {
				screen := ui.renderer.BuildScreen(snapshot, config, false, helpVisible, false, true, nil, UpdateStatus{}, 0)
				if !strings.Contains(screen, test.label) {
					t.Fatalf("expected logging label %q in screen:\n%s", test.label, screen)
				}
				if got := strings.Contains(screen, "[l] enable logging"); got != test.enableHint {
					t.Fatalf("enable logging hint=%v, want %v (helpVisible=%v) in screen:\n%s", got, test.enableHint, helpVisible, screen)
				}
				if got := strings.Contains(screen, "[l] event logging"); got != (test.adHoc && helpVisible) {
					t.Fatalf("event logging hint=%v, want %v in screen:\n%s", got, test.adHoc && helpVisible, screen)
				}
			}
			report := ui.renderer.BuildReport(snapshot, config, false)
			if !strings.Contains(report, "csv_logging: "+test.csvState) {
				t.Fatalf("expected CSV report state %q in report:\n%s", test.csvState, report)
			}
		})
	}
}
