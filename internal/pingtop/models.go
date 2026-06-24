package pingtop

import (
	"errors"
	"net"
	"strings"
	"time"
)

type TargetSpec struct {
	Value string `json:"value"`
	Kind  string `json:"type"`
}

func InferTarget(value string) (TargetSpec, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return TargetSpec{}, errors.New("target cannot be empty")
	}
	if ip := net.ParseIP(raw); ip != nil {
		return TargetSpec{Value: ip.String(), Kind: "ip"}, nil
	}
	hostname := strings.ToLower(strings.TrimSuffix(raw, "."))
	if hostname == "" {
		return TargetSpec{}, errors.New("hostname cannot be empty")
	}
	if strings.ContainsAny(hostname, " \t\r\n") {
		return TargetSpec{}, errors.New("hostname cannot contain whitespace")
	}
	return TargetSpec{Value: hostname, Kind: "hostname"}, nil
}

type CheckResult struct {
	Sequence      int64
	CycleID       int
	Timestamp     time.Time
	Target        string
	TargetType    string
	ResolvedIP    string
	DNSSuccess    *bool
	PingSuccess   bool
	LatencyMS     *float64
	ErrorCategory string
	ErrorMessage  string
	WorkerID      string
}

func (result CheckResult) IsFailure() bool {
	if result.ErrorCategory != "ok" {
		return true
	}
	if result.DNSSuccess != nil && !*result.DNSSuccess {
		return true
	}
	return !result.PingSuccess
}

func (result CheckResult) StatusText() string {
	if result.DNSSuccess != nil && !*result.DNSSuccess {
		return "dns_fail"
	}
	if result.PingSuccess {
		return "up"
	}
	if result.ErrorCategory == "ping_unavailable" {
		return "no_ping"
	}
	return "ping_fail"
}

type BufferedLogResult struct {
	Result  CheckResult
	Written bool
}

type EventEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
}

type DiagnosisAssessment struct {
	Key              string
	ConfirmedMessage string
	SuspectedMessage string
}

type CounterSummary struct {
	Checks      int
	Successes   int
	Failures    int
	DNSFailures int
	PingFailures int
}

func (summary *CounterSummary) Observe(result CheckResult) {
	summary.Checks++
	if result.IsFailure() {
		summary.Failures++
	} else {
		summary.Successes++
	}
	if result.DNSSuccess != nil && !*result.DNSSuccess {
		summary.DNSFailures++
	} else if !result.PingSuccess {
		summary.PingFailures++
	}
}

func (summary *CounterSummary) Add(other CounterSummary) {
	summary.Checks += other.Checks
	summary.Successes += other.Successes
	summary.Failures += other.Failures
	summary.DNSFailures += other.DNSFailures
	summary.PingFailures += other.PingFailures
}

func (summary *CounterSummary) Subtract(other CounterSummary) {
	summary.Checks -= other.Checks
	summary.Successes -= other.Successes
	summary.Failures -= other.Failures
	summary.DNSFailures -= other.DNSFailures
	summary.PingFailures -= other.PingFailures
}

func (summary CounterSummary) Copy() CounterSummary {
	return summary
}

func (summary CounterSummary) LossPercentage() float64 {
	if summary.Checks <= 0 {
		return 0
	}
	return (float64(summary.Failures) / float64(summary.Checks)) * 100
}

type RollingWindowBucket struct {
	BucketStart int64
	Summary     CounterSummary
}

type TargetStats struct {
	Target              string
	TargetType          string
	TotalChecks         int
	SuccessCount        int
	FailureCount        int
	DNSFailureCount     int
	PingFailureCount    int
	ConsecutiveFailures int
	ConsecutiveSuccesses int
	RecoveryPending     bool
	LastState           string
	LastResult          string
	LastLatencyMS       *float64
	LastResolvedIP      string
	LastErrorCategory   string
	LastErrorMessage    string
	LastCheckedAt       time.Time
	WindowSummary       CounterSummary
}

func (stats *TargetStats) Apply(result CheckResult) (string, string) {
	previousState := stats.LastState
	previousError := stats.LastErrorCategory
	stats.TotalChecks++
	stats.LastCheckedAt = result.Timestamp
	if result.ResolvedIP != "" {
		stats.LastResolvedIP = result.ResolvedIP
	}
	stats.LastErrorCategory = result.ErrorCategory
	stats.LastErrorMessage = result.ErrorMessage

	if result.DNSSuccess != nil && !*result.DNSSuccess {
		stats.FailureCount++
		stats.DNSFailureCount++
		stats.ConsecutiveFailures++
		stats.ConsecutiveSuccesses = 0
		stats.RecoveryPending = true
		stats.LastState = "down"
		stats.LastResult = "DNS_FAIL"
		stats.LastLatencyMS = nil
		return previousState, previousError
	}
	if result.PingSuccess {
		stats.SuccessCount++
		stats.ConsecutiveFailures = 0
		stats.ConsecutiveSuccesses++
		stats.LastState = "up"
		stats.LastResult = "UP"
		stats.LastLatencyMS = cloneLatency(result.LatencyMS)
		return previousState, previousError
	}
	stats.FailureCount++
	stats.PingFailureCount++
	stats.ConsecutiveFailures++
	stats.ConsecutiveSuccesses = 0
	stats.RecoveryPending = true
	stats.LastState = "down"
	stats.LastResult = "PING_FAIL"
	stats.LastLatencyMS = nil
	return previousState, previousError
}

func (stats *TargetStats) PacketLossPercentage() float64 {
	if stats.TotalChecks <= 0 {
		return 0
	}
	return (float64(stats.FailureCount) / float64(stats.TotalChecks)) * 100
}

func (stats *TargetStats) ResetCounters() {
	stats.TotalChecks = 0
	stats.SuccessCount = 0
	stats.FailureCount = 0
	stats.DNSFailureCount = 0
	stats.PingFailureCount = 0
	stats.ConsecutiveFailures = 0
	stats.ConsecutiveSuccesses = 0
	stats.RecoveryPending = false
}

type SessionTotals struct {
	StartedAt       time.Time
	LastResetAt     time.Time
	CyclesCompleted int
	TotalChecks     int
	Successes       int
	Failures        int
	DNSFailures     int
	PingFailures    int
}

func NewSessionTotals() SessionTotals {
	now := time.Now()
	return SessionTotals{
		StartedAt:   now,
		LastResetAt: now,
	}
}

func (totals *SessionTotals) Reset() {
	totals.LastResetAt = time.Now()
	totals.CyclesCompleted = 0
	totals.TotalChecks = 0
	totals.Successes = 0
	totals.Failures = 0
	totals.DNSFailures = 0
	totals.PingFailures = 0
}

type StateSnapshot struct {
	Diagnosis            string
	TargetStats          []TargetStats
	RecentEvents         []EventEntry
	Session              SessionTotals
	SessionWindow        CounterSummary
	StatsWindowSeconds   int
	LastCycleCompletedAt time.Time
	LastCycleID          int
}

type PromptState struct {
	Kind    string
	Message string
	Buffer  string
}
