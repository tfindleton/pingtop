package pingtop

import (
	"strings"
	"testing"
	"time"
)

func makeResult(
	target string,
	targetType string,
	cycleID int,
	timestamp int64,
	pingSuccess bool,
	dnsSuccess *bool,
	resolvedIP string,
	errorCategory string,
	latencyMS *float64,
) CheckResult {
	return CheckResult{
		CycleID:       cycleID,
		Timestamp:     time.Unix(timestamp, 0),
		Target:        target,
		TargetType:    targetType,
		ResolvedIP:    resolvedIP,
		DNSSuccess:    dnsSuccess,
		PingSuccess:   pingSuccess,
		LatencyMS:     latencyMS,
		ErrorCategory: errorCategory,
		ErrorMessage:  errorMessageForCategory(errorCategory),
	}
}

func TestDiagnoseCyclePrefersGeneralNetworkIssue(t *testing.T) {
	config := defaultConfig()
	results := []CheckResult{
		makeResult("1.1.1.1", "ip", 1, 1, false, nil, "1.1.1.1", "timeout", nil),
		makeResult("8.8.8.8", "ip", 1, 1, false, nil, "8.8.8.8", "timeout", nil),
		makeResult("google.com", "hostname", 1, 1, false, testBoolPtr(false), "", "dns_failure", nil),
		makeResult("cloudflare.com", "hostname", 1, 1, false, testBoolPtr(false), "", "dns_failure", nil),
		makeResult("apple.com", "hostname", 1, 1, false, testBoolPtr(false), "", "dns_failure", nil),
	}
	assessment := diagnoseCycle(results, config)
	if assessment.Key != "network_issue" {
		t.Fatalf("expected network issue, got %q", assessment.Key)
	}
}

func TestStateStoreRequiresConfirmationForFailureAndRecovery(t *testing.T) {
	config := defaultConfig()
	config.DiagnosisConfirmCycles = 2
	config.RecoveryConfirmCycles = 2
	store := NewStateStore(config)

	failCycle := []CheckResult{
		makeResult("1.1.1.1", "ip", 1, 1, false, nil, "1.1.1.1", "timeout", nil),
		makeResult("8.8.8.8", "ip", 1, 1, false, nil, "8.8.8.8", "timeout", nil),
		makeResult("google.com", "hostname", 1, 1, false, testBoolPtr(false), "", "dns_failure", nil),
		makeResult("cloudflare.com", "hostname", 1, 1, false, testBoolPtr(false), "", "dns_failure", nil),
		makeResult("apple.com", "hostname", 1, 1, false, testBoolPtr(false), "", "dns_failure", nil),
	}
	latency20 := 20.0
	latency21 := 21.0
	latency22 := 22.0
	latency23 := 23.0
	latency24 := 24.0
	okCycle := []CheckResult{
		makeResult("1.1.1.1", "ip", 2, 2, true, nil, "1.1.1.1", "ok", &latency20),
		makeResult("8.8.8.8", "ip", 2, 2, true, nil, "8.8.8.8", "ok", &latency21),
		makeResult("google.com", "hostname", 2, 2, true, testBoolPtr(true), "142.250.0.1", "ok", &latency22),
		makeResult("cloudflare.com", "hostname", 2, 2, true, testBoolPtr(true), "104.16.0.1", "ok", &latency23),
		makeResult("apple.com", "hostname", 2, 2, true, testBoolPtr(true), "17.253.144.10", "ok", &latency24),
	}

	store.HandleCycle(failCycle, config, 1)
	if got := store.Snapshot().Diagnosis; got != "Suspected general network issue (1/2)" {
		t.Fatalf("unexpected diagnosis after first fail cycle: %q", got)
	}
	store.HandleCycle(failCycle, config, 2)
	if got := store.Snapshot().Diagnosis; got != "Likely general network issue" {
		t.Fatalf("unexpected diagnosis after second fail cycle: %q", got)
	}
	store.HandleCycle(okCycle, config, 3)
	if got := store.Snapshot().Diagnosis; got != "Recovery observed, confirming stability (1/2)" {
		t.Fatalf("unexpected diagnosis after first recovery cycle: %q", got)
	}
	store.HandleCycle(okCycle, config, 4)
	snapshot := store.Snapshot()
	if snapshot.Diagnosis != "All monitored targets are reachable" {
		t.Fatalf("unexpected final diagnosis: %q", snapshot.Diagnosis)
	}
	foundRecovery := false
	for _, event := range snapshot.RecentEvents {
		if strings.Contains(event.Message, "recovered") {
			foundRecovery = true
			break
		}
	}
	if !foundRecovery {
		t.Fatal("expected recovery event to be recorded")
	}
}

func TestStateStoreChangesOnlySuppressesRepeatedFailureEvents(t *testing.T) {
	config := defaultConfig()
	config.Targets = []TargetSpec{{Value: "1.1.1.1", Kind: "ip"}}
	config.LoggingMode = LoggingModeChangesOnly
	config.DiagnosisConfirmCycles = 1
	store := NewStateStore(config)

	for cycleID := 1; cycleID <= 3; cycleID++ {
		store.HandleCycle([]CheckResult{
			makeResult("1.1.1.1", "ip", cycleID, int64(cycleID), false, nil, "1.1.1.1", "timeout", nil),
		}, config, cycleID)
	}
	store.HandleCycle([]CheckResult{
		makeResult("1.1.1.1", "ip", 4, 4, false, nil, "1.1.1.1", "ping_failure", nil),
	}, config, 4)

	warnings := 0
	for _, event := range store.Snapshot().RecentEvents {
		if event.Level == "warn" && strings.Contains(event.Message, "1.1.1.1 ping_fail") {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("expected first failure and changed failure events only, got %d warnings", warnings)
	}
}

func TestStateStoreLoggingOffSuppressesAutomaticEvents(t *testing.T) {
	config := defaultConfig()
	config.Targets = []TargetSpec{{Value: "1.1.1.1", Kind: "ip"}}
	config.LoggingMode = LoggingModeOff
	config.DiagnosisConfirmCycles = 1
	store := NewStateStore(config)

	store.HandleCycle([]CheckResult{
		makeResult("1.1.1.1", "ip", 1, 1, false, nil, "1.1.1.1", "timeout", nil),
	}, config, 1)

	if events := store.Snapshot().RecentEvents; len(events) != 0 {
		t.Fatalf("expected logging off to suppress automatic events, got %#v", events)
	}
}

func TestStateStoreIgnoresProgressFromOldGeneration(t *testing.T) {
	config := defaultConfig()
	config.Targets = []TargetSpec{{Value: "1.1.1.1", Kind: "ip"}}
	store := NewStateStore(config)

	store.BeginCycle(1, 1, config, time.Unix(1, 0))
	store.ClearActiveCycle()
	store.BeginCycle(2, 2, config, time.Unix(2, 0))
	store.NoteCycleProgress(1, 1, makeResult("1.1.1.1", "ip", 1, 1, true, nil, "1.1.1.1", "ok", nil))

	snapshot := store.Snapshot()
	if !snapshot.ActiveCycle.Active || snapshot.ActiveCycle.CycleID != 2 || snapshot.ActiveCycle.Generation != 2 {
		t.Fatalf("unexpected active cycle after generation change: %#v", snapshot.ActiveCycle)
	}
	if snapshot.ActiveCycle.CompletedChecks != 0 {
		t.Fatalf("expected old generation progress to be ignored, got %d", snapshot.ActiveCycle.CompletedChecks)
	}
	if !snapshot.TargetStats[0].Checking {
		t.Fatal("expected current generation target to remain checking")
	}
}

func TestStateStoreClearsCheckingForCompletedTargetOnly(t *testing.T) {
	config := defaultConfig()
	config.Targets = []TargetSpec{
		{Value: "1.1.1.1", Kind: "ip"},
		{Value: "8.8.8.8", Kind: "ip"},
	}
	store := NewStateStore(config)

	store.BeginCycle(1, 1, config, time.Unix(1, 0))
	store.NoteCycleProgress(1, 1, makeResult("1.1.1.1", "ip", 1, 1, true, nil, "1.1.1.1", "ok", nil))

	snapshot := store.Snapshot()
	if snapshot.ActiveCycle.CompletedChecks != 1 {
		t.Fatalf("expected one completed check, got %d", snapshot.ActiveCycle.CompletedChecks)
	}
	if snapshot.TargetStats[0].Checking {
		t.Fatal("expected completed target to stop showing checking")
	}
	if !snapshot.TargetStats[1].Checking {
		t.Fatal("expected unfinished target to remain checking")
	}
}

func TestRollingWindowCounterPrunesOldBuckets(t *testing.T) {
	counter := NewRollingWindowCounter(30)
	result := makeResult("1.1.1.1", "ip", 1, 0, true, nil, "1.1.1.1", "ok", nil)
	counter.Observe(time.Unix(0, 0), result)
	counter.Observe(time.Unix(15, 0), result)
	if got := counter.Snapshot(time.Unix(20, 0)).Checks; got != 2 {
		t.Fatalf("expected 2 checks at t=20, got %d", got)
	}
	if got := counter.Snapshot(time.Unix(31, 0)).Checks; got != 1 {
		t.Fatalf("expected 1 check at t=31, got %d", got)
	}
}

func TestStateStoreRevisionChangesOnMutations(t *testing.T) {
	config := defaultConfig()
	store := NewStateStore(config)
	initial := store.Revision()

	store.AddEvent("info", "hello", time.Unix(1, 0))
	if got := store.Revision(); got <= initial {
		t.Fatalf("expected revision to increase after AddEvent: initial=%d got=%d", initial, got)
	}

	afterEvent := store.Revision()
	store.ResetCounters()
	if got := store.Revision(); got <= afterEvent {
		t.Fatalf("expected revision to increase after ResetCounters: afterEvent=%d got=%d", afterEvent, got)
	}
}
