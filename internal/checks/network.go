package checks

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

type AppConfig = pingtop.AppConfig
type CheckResult = pingtop.CheckResult
type TargetSpec = pingtop.TargetSpec

var shorten = pingtop.Shorten

var latencyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)time[=<]?\s*(\d+(?:\.\d+)?)\s*ms`),
	regexp.MustCompile(`(?i)tempo[=<]?\s*(\d+(?:\.\d+)?)\s*ms`),
	regexp.MustCompile(`(?i)temps?[=<]?\s*(\d+(?:\.\d+)?)\s*ms`),
	regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*ms`),
}

type PingRunner struct {
	goos      string
	pingPath  string
	ping6Path string
}

func NewPingRunner() *PingRunner {
	pingPath, _ := exec.LookPath("ping")
	ping6Path, _ := exec.LookPath("ping6")
	return &PingRunner{
		goos:      runtime.GOOS,
		pingPath:  pingPath,
		ping6Path: ping6Path,
	}
}

func (runner *PingRunner) Ping(ipAddress string, timeoutMS int) (bool, *float64, string, string) {
	return runner.PingContext(context.Background(), ipAddress, timeoutMS)
}

func (runner *PingRunner) PingContext(parent context.Context, ipAddress string, timeoutMS int) (bool, *float64, string, string) {
	if err := parent.Err(); err != nil {
		return false, nil, "canceled", err.Error()
	}
	if success, latencyMS, errorCategory, errorMessage, handled := nativePingContext(parent, ipAddress, timeoutMS); handled {
		return success, latencyMS, errorCategory, errorMessage
	}

	command := runner.buildCommand(ipAddress, timeoutMS)
	timeout := time.Duration(math.Max(3.0, float64(timeoutMS)/1000.0+2.0) * float64(time.Second))
	if command[0] == "ping6" {
		// macOS ping6 has no per-probe timeout option; -W is a node-info
		// query. Bound the command itself by the configured probe timeout.
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	started := time.Now()
	commandPath := runner.commandPath(command[0])
	cmd := exec.CommandContext(ctx, commandPath, command[1:]...)
	output, err := cmd.CombinedOutput()
	elapsedMS := float64(time.Since(started)) / float64(time.Millisecond)

	if err == nil {
		parsedLatency := runner.parseLatency(string(output))
		if parsedLatency != nil {
			return true, parsedLatency, "ok", ""
		}
		latency := elapsedMS
		return true, &latency, "ok", ""
	}

	if ctx.Err() == context.DeadlineExceeded {
		return false, nil, "timeout", fmt.Sprintf("ping command exceeded %d ms timeout", timeoutMS)
	}
	if ctx.Err() != nil {
		return false, nil, "canceled", ctx.Err().Error()
	}

	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return false, nil, "ping_unavailable", "system ping command not found"
	}

	combinedOutput := string(output)
	category := "ping_failure"
	lowered := strings.ToLower(combinedOutput)
	if strings.Contains(lowered, "timed out") || strings.Contains(lowered, "100% packet loss") || strings.Contains(lowered, "100% loss") {
		category = "timeout"
	}
	message := runner.summarizeError(combinedOutput)
	if message == "" {
		message = "ping exited with non-zero status"
	}
	return false, nil, category, message
}

func (runner *PingRunner) buildCommand(ipAddress string, timeoutMS int) []string {
	switch runner.goos {
	case "windows":
		return []string{"ping", "-n", "1", "-w", fmt.Sprintf("%d", timeoutMS), ipAddress}
	case "darwin":
		if ip := net.ParseIP(ipAddress); ip != nil && ip.To4() == nil {
			// ping6 otherwise exits after its default interval plus a fixed
			// ten-second final wait. Keep that exit beyond our context deadline;
			// -c 1 still sends exactly one probe and a reply exits immediately.
			intervalSeconds := int(math.Max(1, math.Ceil(float64(timeoutMS)/1000.0)))
			return []string{"ping6", "-n", "-c", "1", "-i", strconv.Itoa(intervalSeconds), ipAddress}
		}
		return []string{"ping", "-n", "-c", "1", "-W", fmt.Sprintf("%d", timeoutMS), ipAddress}
	default:
		timeoutSeconds := int(math.Max(1, math.Ceil(float64(timeoutMS)/1000.0)))
		return []string{"ping", "-n", "-c", "1", "-W", fmt.Sprintf("%d", timeoutSeconds), ipAddress}
	}
}

func (runner *PingRunner) commandPath(command string) string {
	if command == "ping6" && runner.ping6Path != "" {
		return runner.ping6Path
	}
	if command == "ping" && runner.pingPath != "" {
		return runner.pingPath
	}
	return command
}

func (runner *PingRunner) parseLatency(output string) *float64 {
	for _, pattern := range latencyPatterns {
		match := pattern.FindStringSubmatch(output)
		if len(match) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err == nil {
			return &value
		}
	}
	return nil
}

func (runner *PingRunner) summarizeError(output string) string {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return ""
	}
	return shorten(strings.Join(fields, " "), 180)
}

type dnsLookupResult struct {
	ok      bool
	address string
	err     string
}

type DNSLookupFunc func(context.Context, string) (bool, string, string)

type DNSResolver struct {
	lookupFunc DNSLookupFunc
}

func NewDNSResolver(lookupFunc DNSLookupFunc) *DNSResolver {
	if lookupFunc == nil {
		lookupFunc = blockingResolveHostname
	}
	return &DNSResolver{
		lookupFunc: lookupFunc,
	}
}

func (resolver *DNSResolver) Resolve(hostname string, timeoutMS int) (bool, string, string) {
	return resolver.ResolveContext(context.Background(), hostname, timeoutMS)
}

func (resolver *DNSResolver) ResolveContext(parent context.Context, hostname string, timeoutMS int) (bool, string, string) {
	if err := parent.Err(); err != nil {
		return false, "", err.Error()
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	resultCh := make(chan dnsLookupResult, 1)
	go func() {
		ok, address, errMessage := resolver.lookupFunc(ctx, hostname)
		result := dnsLookupResult{ok: ok, address: address, err: errMessage}
		select {
		case resultCh <- result:
		case <-ctx.Done():
		}
	}()

	select {
	case result := <-resultCh:
		if err := parent.Err(); err != nil {
			return false, "", err.Error()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, "", fmt.Sprintf("dns lookup exceeded %d ms timeout", timeoutMS)
		}
		return result.ok, result.address, result.err
	case <-ctx.Done():
		if err := parent.Err(); err != nil {
			return false, "", err.Error()
		}
		return false, "", fmt.Sprintf("dns lookup exceeded %d ms timeout", timeoutMS)
	}
}

var defaultDNSResolver = NewDNSResolver(nil)

func blockingResolveHostname(ctx context.Context, hostname string) (bool, string, string) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return false, "", err.Error()
	}
	unique := make([]string, 0, len(addresses))
	seen := make(map[string]struct{})
	for _, address := range addresses {
		value := address.IP.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	if len(unique) == 0 {
		return false, "", "no DNS answers returned"
	}
	preferred := unique[0]
	for _, candidate := range unique {
		if ip := net.ParseIP(candidate); ip != nil && ip.To4() != nil {
			preferred = candidate
			break
		}
	}
	return true, preferred, ""
}

type CheckCoordinator struct {
	pingRunner  *PingRunner
	dnsResolver *DNSResolver
}

type cycleResult struct {
	index  int
	result CheckResult
}

func NewCheckCoordinator(pingRunner *PingRunner, dnsResolver *DNSResolver) *CheckCoordinator {
	if dnsResolver == nil {
		dnsResolver = defaultDNSResolver
	}
	return &CheckCoordinator{
		pingRunner:  pingRunner,
		dnsResolver: dnsResolver,
	}
}

func (coordinator *CheckCoordinator) Close() {
	if coordinator.pingRunner != nil {
		coordinator.pingRunner.Close()
	}
}

func (coordinator *CheckCoordinator) ExecuteCycle(config AppConfig, cycleID int) []CheckResult {
	return coordinator.ExecuteCycleContext(context.Background(), config, cycleID, nil)
}

func (coordinator *CheckCoordinator) ExecuteCycleContext(ctx context.Context, config AppConfig, cycleID int, onProgress func(CheckResult)) []CheckResult {
	targets := config.EnabledTargets()
	if len(targets) == 0 {
		return nil
	}
	results := make([]CheckResult, len(targets))
	completedResults := make([]bool, len(targets))
	resultCh := make(chan cycleResult, len(targets))
	for index, target := range targets {
		go func(index int, target TargetSpec) {
			result := coordinator.safeCheckTarget(ctx, target, config.PingTimeoutMS, cycleID, fmt.Sprintf("worker-%d", index+1))
			// Each worker has one buffered slot, including when the caller stops.
			// Cancellation must not randomly discard an already completed result.
			resultCh <- cycleResult{index: index, result: result}
		}(index, target)
	}

	recordResult := func(item cycleResult) {
		if item.result.ErrorCategory == "canceled" {
			return
		}
		results[item.index] = item.result
		completedResults[item.index] = true
		if onProgress != nil {
			onProgress(item.result)
		}
	}
	collectedResults := func() []CheckResult {
		var collected []CheckResult
		for index, completed := range completedResults {
			if completed {
				collected = append(collected, results[index])
			}
		}
		return collected
	}
	for completed := 0; completed < len(targets); completed++ {
		select {
		case item := <-resultCh:
			recordResult(item)
		case <-ctx.Done():
			// Retain results already delivered by fast targets while another target
			// was pending. Do not wait for unfinished checks after cancellation.
			for {
				select {
				case item := <-resultCh:
					recordResult(item)
				default:
					return collectedResults()
				}
			}
		}
	}
	return collectedResults()
}

func (coordinator *CheckCoordinator) CheckTargetContext(ctx context.Context, target TargetSpec, timeoutMS, cycleID int, workerID string) CheckResult {
	return coordinator.safeCheckTarget(ctx, target, timeoutMS, cycleID, workerID)
}

func (coordinator *CheckCoordinator) safeCheckTarget(ctx context.Context, target TargetSpec, timeoutMS, cycleID int, workerID string) (result CheckResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			var dnsSuccess *bool
			if target.Kind != "ip" {
				dnsSuccess = boolPtr(false)
			}
			result = CheckResult{
				CycleID:       cycleID,
				Timestamp:     time.Now(),
				Target:        target.Value,
				TargetType:    target.Kind,
				ResolvedIP:    fallbackResolvedIP(target),
				DNSSuccess:    dnsSuccess,
				PingSuccess:   false,
				ErrorCategory: "internal_error",
				ErrorMessage:  shorten(fmt.Sprint(recovered), 180),
				WorkerID:      workerID,
			}
		}
	}()
	return coordinator.checkTarget(ctx, target, timeoutMS, cycleID, workerID)
}

func (coordinator *CheckCoordinator) checkTarget(ctx context.Context, target TargetSpec, timeoutMS, cycleID int, workerID string) CheckResult {
	if err := ctx.Err(); err != nil {
		return canceledResult(target, cycleID, workerID, err)
	}
	if target.Kind == "ip" {
		pingSuccess, latencyMS, errorCategory, errorMessage := coordinator.pingRunner.PingContext(ctx, target.Value, timeoutMS)
		return CheckResult{
			CycleID:       cycleID,
			Timestamp:     time.Now(),
			Target:        target.Value,
			TargetType:    "ip",
			ResolvedIP:    target.Value,
			DNSSuccess:    nil,
			PingSuccess:   pingSuccess,
			LatencyMS:     latencyMS,
			ErrorCategory: chooseOKCategory(pingSuccess, errorCategory),
			ErrorMessage:  errorMessage,
			WorkerID:      workerID,
		}
	}

	dnsSuccess, resolvedIP, dnsError := coordinator.dnsResolver.ResolveContext(ctx, target.Value, timeoutMS)
	if err := ctx.Err(); err != nil {
		return canceledResult(target, cycleID, workerID, err)
	}
	if !dnsSuccess {
		category := "dns_failure"
		lowered := strings.ToLower(dnsError)
		if strings.Contains(lowered, "timeout") || strings.Contains(lowered, "pending") {
			category = "dns_timeout"
		}
		return CheckResult{
			CycleID:       cycleID,
			Timestamp:     time.Now(),
			Target:        target.Value,
			TargetType:    "hostname",
			ResolvedIP:    "",
			DNSSuccess:    boolPtr(false),
			PingSuccess:   false,
			LatencyMS:     nil,
			ErrorCategory: category,
			ErrorMessage:  shorten(dnsError, 180),
			WorkerID:      workerID,
		}
	}

	pingSuccess, latencyMS, errorCategory, errorMessage := coordinator.pingRunner.PingContext(ctx, resolvedIP, timeoutMS)
	return CheckResult{
		CycleID:       cycleID,
		Timestamp:     time.Now(),
		Target:        target.Value,
		TargetType:    "hostname",
		ResolvedIP:    resolvedIP,
		DNSSuccess:    boolPtr(true),
		PingSuccess:   pingSuccess,
		LatencyMS:     latencyMS,
		ErrorCategory: chooseOKCategory(pingSuccess, errorCategory),
		ErrorMessage:  errorMessage,
		WorkerID:      workerID,
	}
}

func resolveHostname(hostname string, timeoutMS int) (bool, string, string) {
	return defaultDNSResolver.Resolve(hostname, timeoutMS)
}

func canceledResult(target TargetSpec, cycleID int, workerID string, err error) CheckResult {
	var dnsSuccess *bool
	if target.Kind != "ip" {
		dnsSuccess = boolPtr(false)
	}
	return CheckResult{
		CycleID:       cycleID,
		Timestamp:     time.Now(),
		Target:        target.Value,
		TargetType:    target.Kind,
		ResolvedIP:    fallbackResolvedIP(target),
		DNSSuccess:    dnsSuccess,
		PingSuccess:   false,
		LatencyMS:     nil,
		ErrorCategory: "canceled",
		ErrorMessage:  shorten(err.Error(), 180),
		WorkerID:      workerID,
	}
}

func chooseOKCategory(success bool, category string) string {
	if success {
		return "ok"
	}
	return category
}

func boolPtr(value bool) *bool {
	copyValue := value
	return &copyValue
}

func fallbackResolvedIP(target TargetSpec) string {
	if target.Kind == "ip" {
		return target.Value
	}
	return ""
}

func (runner *PingRunner) Close() {}
