package pingtop

import (
	"fmt"
	"sync"
	"time"
)

type RollingWindowCounter struct {
	windowSeconds int
	bucketSeconds int
	buckets       []RollingWindowBucket
	total         CounterSummary
}

func NewRollingWindowCounter(windowSeconds int) *RollingWindowCounter {
	return &RollingWindowCounter{
		windowSeconds: windowSeconds,
		bucketSeconds: chooseBucketSeconds(windowSeconds),
		buckets:       make([]RollingWindowBucket, 0),
	}
}

func (counter *RollingWindowCounter) Observe(timestamp time.Time, result CheckResult) {
	bucketStart := (timestamp.Unix() / int64(counter.bucketSeconds)) * int64(counter.bucketSeconds)
	if len(counter.buckets) > 0 && counter.buckets[len(counter.buckets)-1].BucketStart == bucketStart {
		counter.buckets[len(counter.buckets)-1].Summary.Observe(result)
	} else {
		bucket := RollingWindowBucket{BucketStart: bucketStart}
		bucket.Summary.Observe(result)
		counter.buckets = append(counter.buckets, bucket)
	}
	counter.total.Observe(result)
	counter.Prune(timestamp)
}

func (counter *RollingWindowCounter) Prune(now time.Time) {
	cutoff := now.Unix() - int64(counter.windowSeconds)
	expiredCount := 0
	for expiredCount < len(counter.buckets) {
		expired := counter.buckets[expiredCount]
		if expired.BucketStart+int64(counter.bucketSeconds) > cutoff {
			break
		}
		counter.total.Subtract(expired.Summary)
		expiredCount++
	}
	if expiredCount > 0 {
		copy(counter.buckets, counter.buckets[expiredCount:])
		clear(counter.buckets[len(counter.buckets)-expiredCount:])
		counter.buckets = counter.buckets[:len(counter.buckets)-expiredCount]
	}
}

func (counter *RollingWindowCounter) Snapshot(now time.Time) CounterSummary {
	counter.Prune(now)
	return counter.total.Copy()
}

func chooseBucketSeconds(windowSeconds int) int {
	candidates := []int{1, 5, 10, 15, 30, 60, 300, 900, 1800, 3600, 7200, 21600, 43200, 86400}
	for _, candidate := range candidates {
		if float64(windowSeconds)/float64(candidate) <= 240 {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

type StateStore struct {
	mu                     sync.RWMutex
	events                 []EventEntry
	eventHistorySize       int
	stats                  map[string]*TargetStats
	targetWindows          map[string]*RollingWindowCounter
	targetOrder            []string
	session                SessionTotals
	sessionWindow          *RollingWindowCounter
	statsWindowSeconds     int
	diagnosis              string
	confirmedDiagnosis     string
	pendingDiagnosis       string
	pendingDiagnosisStreak int
	activeCycle            CycleStatus
	lastCycleCompletedAt   time.Time
	lastCycleID            int
	revision               uint64
}

func NewStateStore(config AppConfig) *StateStore {
	store := &StateStore{
		events:             make([]EventEntry, 0, config.EventHistorySize),
		eventHistorySize:   config.EventHistorySize,
		stats:              make(map[string]*TargetStats),
		targetWindows:      make(map[string]*RollingWindowCounter),
		targetOrder:        make([]string, 0, len(config.Targets)),
		session:            NewSessionTotals(),
		sessionWindow:      NewRollingWindowCounter(config.StatsWindowSeconds),
		statsWindowSeconds: config.StatsWindowSeconds,
		diagnosis:          "Waiting for first cycle",
		confirmedDiagnosis: "waiting",
		pendingDiagnosis:   "waiting",
		revision:           1,
	}
	store.syncTargetsLocked(config)
	return store
}

func (store *StateStore) SyncTargets(config AppConfig) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	windowReset := store.syncTargetsLocked(config)
	store.revision++
	return windowReset
}

func (store *StateStore) syncTargetsLocked(config AppConfig) bool {
	windowReset := false
	if store.eventHistorySize != config.EventHistorySize {
		store.eventHistorySize = config.EventHistorySize
		store.events = trimEventEntries(store.events, store.eventHistorySize)
	}
	if store.statsWindowSeconds != config.StatsWindowSeconds {
		store.statsWindowSeconds = config.StatsWindowSeconds
		store.sessionWindow = NewRollingWindowCounter(config.StatsWindowSeconds)
		windowReset = true
	}

	orderedStats := make(map[string]*TargetStats, len(config.Targets))
	orderedWindows := make(map[string]*RollingWindowCounter, len(config.Targets))
	order := make([]string, 0, len(config.Targets))

	for _, target := range config.Targets {
		stats := store.stats[target.Value]
		if stats == nil {
			stats = &TargetStats{
				Target:     target.Value,
				TargetType: target.Kind,
				LastState:  "unknown",
				LastResult: "pending",
			}
		} else {
			stats.TargetType = target.Kind
		}
		orderedStats[target.Value] = stats
		if windowReset {
			orderedWindows[target.Value] = NewRollingWindowCounter(store.statsWindowSeconds)
		} else if existing := store.targetWindows[target.Value]; existing != nil {
			orderedWindows[target.Value] = existing
		} else {
			orderedWindows[target.Value] = NewRollingWindowCounter(store.statsWindowSeconds)
		}
		order = append(order, target.Value)
	}

	store.stats = orderedStats
	store.targetWindows = orderedWindows
	store.targetOrder = order
	return windowReset
}

func (store *StateStore) AddEvent(level, message string, timestamp time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.addEventLocked(level, message, timestamp)
	store.revision++
}

func (store *StateStore) BeginCycle(cycleID int, generation int64, config AppConfig, timestamp time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.syncTargetsLocked(config)
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	store.activeCycle = CycleStatus{
		Active:          true,
		CycleID:         cycleID,
		Generation:      generation,
		StartedAt:       timestamp,
		TotalChecks:     len(config.Targets),
		CompletedChecks: 0,
	}
	for _, target := range config.Targets {
		if stats := store.stats[target.Value]; stats != nil {
			stats.Checking = true
		}
	}
	store.revision++
}

func (store *StateStore) NoteCycleProgress(cycleID int, generation int64, result CheckResult) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.activeCycle.Active || store.activeCycle.CycleID != cycleID || store.activeCycle.Generation != generation {
		return
	}
	if store.activeCycle.CompletedChecks < store.activeCycle.TotalChecks {
		store.activeCycle.CompletedChecks++
	}
	if stats := store.stats[result.Target]; stats != nil {
		stats.Checking = false
	}
	store.revision++
}

func (store *StateStore) FinishCycle(cycleID int, generation int64) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.activeCycle.Active || store.activeCycle.CycleID != cycleID || store.activeCycle.Generation != generation {
		return
	}
	store.clearActiveCycleLocked()
	store.revision++
}

func (store *StateStore) ClearActiveCycle() {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.activeCycle.Active {
		return
	}
	store.clearActiveCycleLocked()
	store.revision++
}

func (store *StateStore) clearActiveCycleLocked() {
	store.activeCycle = CycleStatus{}
	for _, stats := range store.stats {
		stats.Checking = false
	}
}

func (store *StateStore) addEventLocked(level, message string, timestamp time.Time) {
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	store.events = append(store.events, EventEntry{
		Timestamp: timestamp,
		Level:     level,
		Message:   message,
	})
	store.events = trimEventEntries(store.events, store.eventHistorySize)
}

func (store *StateStore) HandleCycle(results []CheckResult, config AppConfig, cycleID int) {
	store.mu.Lock()
	defer store.mu.Unlock()

	windowReset := store.syncTargetsLocked(config)
	if config.LoggingMode != LoggingModeOff && windowReset && store.session.CyclesCompleted > 0 {
		store.addEventLocked(
			"info",
			fmt.Sprintf("Stats window changed to %s; rolling counters reset", FormatCompactSpan(store.statsWindowSeconds)),
			time.Time{},
		)
	}

	store.session.CyclesCompleted++
	store.lastCycleID = cycleID
	if len(results) > 0 {
		latest := results[0].Timestamp
		for _, result := range results[1:] {
			if result.Timestamp.After(latest) {
				latest = result.Timestamp
			}
		}
		store.lastCycleCompletedAt = latest
	} else {
		store.lastCycleCompletedAt = time.Now()
	}

	for _, result := range results {
		stats := store.stats[result.Target]
		if stats == nil {
			stats = &TargetStats{
				Target:     result.Target,
				TargetType: result.TargetType,
				LastState:  "unknown",
				LastResult: "pending",
			}
			store.stats[result.Target] = stats
			store.targetOrder = append(store.targetOrder, result.Target)
		}

		previousState, previousError := stats.Apply(result)
		store.sessionWindow.Observe(result.Timestamp, result)
		window := store.targetWindows[result.Target]
		if window == nil {
			window = NewRollingWindowCounter(store.statsWindowSeconds)
			store.targetWindows[result.Target] = window
		}
		window.Observe(result.Timestamp, result)

		store.session.TotalChecks++
		if result.IsFailure() {
			store.session.Failures++
		} else {
			store.session.Successes++
		}
		if result.DNSSuccess != nil && !*result.DNSSuccess {
			store.session.DNSFailures++
		} else if !result.PingSuccess {
			store.session.PingFailures++
		}

		if result.PingSuccess && stats.RecoveryPending && stats.ConsecutiveSuccesses >= config.RecoveryConfirmCycles {
			if config.LoggingMode != LoggingModeOff {
				store.addEventLocked(
					"info",
					fmt.Sprintf("%s recovered (%s)", result.Target, FormatLatency(result.LatencyMS)),
					result.Timestamp,
				)
			}
			stats.RecoveryPending = false
		} else if result.IsFailure() {
			if shouldReportFailureEvent(previousState, previousError, stats, result, config) {
				store.addEventLocked(
					"warn",
					fmt.Sprintf("%s %s: %s", result.Target, result.StatusText(), HumanErrorMessage(result)),
					result.Timestamp,
				)
			}
		}
	}

	assessment := diagnoseCycle(results, config)
	if store.updateDiagnosisLocked(assessment, config) && config.LoggingMode != LoggingModeOff {
		store.addEventLocked("info", "Diagnosis changed: "+store.diagnosis, store.lastCycleCompletedAt)
	}
	store.clearActiveCycleLocked()
	store.revision++
}

func (store *StateStore) ResetCounters() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.session.Reset()
	for _, stats := range store.stats {
		stats.ResetCounters()
		stats.WindowSummary = CounterSummary{}
	}
	store.sessionWindow = NewRollingWindowCounter(store.statsWindowSeconds)
	store.targetWindows = make(map[string]*RollingWindowCounter, len(store.stats))
	for target := range store.stats {
		store.targetWindows[target] = NewRollingWindowCounter(store.statsWindowSeconds)
	}
	store.revision++
}

func (store *StateStore) Revision() uint64 {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.revision
}

func (store *StateStore) Snapshot() StateSnapshot {
	store.mu.RLock()
	defer store.mu.RUnlock()

	now := time.Now()
	targetStats := make([]TargetStats, 0, len(store.targetOrder))
	for _, key := range store.targetOrder {
		stats := store.stats[key]
		if stats == nil {
			continue
		}
		copyStats := *stats
		copyStats.LastLatencyMS = cloneLatency(stats.LastLatencyMS)
		if window := store.targetWindows[key]; window != nil {
			copyStats.WindowSummary = window.Snapshot(now)
		}
		targetStats = append(targetStats, copyStats)
	}

	events := append([]EventEntry(nil), store.events...)
	return StateSnapshot{
		Diagnosis:            store.diagnosis,
		TargetStats:          targetStats,
		RecentEvents:         events,
		Session:              store.session,
		SessionWindow:        store.sessionWindow.Snapshot(now),
		StatsWindowSeconds:   store.statsWindowSeconds,
		LastCycleCompletedAt: store.lastCycleCompletedAt,
		LastCycleID:          store.lastCycleID,
		ActiveCycle:          store.activeCycle,
	}
}

func (store *StateStore) updateDiagnosisLocked(assessment DiagnosisAssessment, config AppConfig) bool {
	if assessment.Key == store.pendingDiagnosis {
		store.pendingDiagnosisStreak++
	} else {
		store.pendingDiagnosis = assessment.Key
		store.pendingDiagnosisStreak = 1
	}

	requiredCycles := store.requiredDiagnosisCyclesLocked(assessment.Key, config)
	if assessment.Key == store.confirmedDiagnosis {
		store.diagnosis = assessment.ConfirmedMessage
		return false
	}

	if store.pendingDiagnosisStreak >= requiredCycles {
		store.confirmedDiagnosis = assessment.Key
		store.diagnosis = assessment.ConfirmedMessage
		return true
	}

	if assessment.Key == "healthy" {
		store.diagnosis = fmt.Sprintf(
			"Recovery observed, confirming stability (%d/%d)",
			store.pendingDiagnosisStreak,
			requiredCycles,
		)
		return false
	}

	store.diagnosis = fmt.Sprintf(
		"Suspected %s (%d/%d)",
		assessment.SuspectedMessage,
		store.pendingDiagnosisStreak,
		requiredCycles,
	)
	return false
}

func (store *StateStore) requiredDiagnosisCyclesLocked(assessmentKey string, config AppConfig) int {
	if assessmentKey == "waiting" || assessmentKey == "no_targets" {
		return 1
	}
	if assessmentKey == "healthy" {
		if store.confirmedDiagnosis == "waiting" || store.confirmedDiagnosis == "no_targets" || store.confirmedDiagnosis == "healthy" {
			return 1
		}
		return config.RecoveryConfirmCycles
	}
	return config.DiagnosisConfirmCycles
}

func shouldReportFailureEvent(previousState, previousError string, stats *TargetStats, result CheckResult, config AppConfig) bool {
	if config.LoggingMode == LoggingModeOff {
		return false
	}
	if config.LoggingMode == LoggingModeChangesOnly {
		return previousState != "down" || previousError != result.ErrorCategory
	}
	return previousState != "down" ||
		previousError != result.ErrorCategory ||
		stats.ConsecutiveFailures == 1 ||
		stats.ConsecutiveFailures == 2 ||
		stats.ConsecutiveFailures == 3 ||
		stats.ConsecutiveFailures%5 == 0
}

func trimEventEntries(events []EventEntry, keep int) []EventEntry {
	if keep <= 0 {
		clear(events)
		return events[:0]
	}
	if len(events) <= keep {
		return events
	}
	drop := len(events) - keep
	copy(events, events[drop:])
	clear(events[keep:])
	return events[:keep]
}
