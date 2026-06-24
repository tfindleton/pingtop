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
	"sync"
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
	goos     string
	pingPath string
}

func NewPingRunner() *PingRunner {
	pingPath, _ := exec.LookPath("ping")
	return &PingRunner{
		goos:     runtime.GOOS,
		pingPath: pingPath,
	}
}

func (runner *PingRunner) Ping(ipAddress string, timeoutMS int) (bool, *float64, string, string) {
	if success, latencyMS, errorCategory, errorMessage, handled := nativePing(ipAddress, timeoutMS); handled {
		return success, latencyMS, errorCategory, errorMessage
	}

	command := runner.buildCommand(ipAddress, timeoutMS)
	timeout := time.Duration(math.Max(3.0, float64(timeoutMS)/1000.0+2.0) * float64(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	started := time.Now()
	commandPath := command[0]
	if runner.pingPath != "" {
		commandPath = runner.pingPath
	}
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
		return []string{"ping", "-n", "-c", "1", "-W", fmt.Sprintf("%d", timeoutMS), ipAddress}
	default:
		timeoutSeconds := int(math.Max(1, math.Ceil(float64(timeoutMS)/1000.0)))
		return []string{"ping", "-n", "-c", "1", "-W", fmt.Sprintf("%d", timeoutSeconds), ipAddress}
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
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
		if !result.ok && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, "", fmt.Sprintf("dns lookup exceeded %d ms timeout", timeoutMS)
		}
		return result.ok, result.address, result.err
	case <-ctx.Done():
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
	if len(config.Targets) == 0 {
		return nil
	}
	results := make([]CheckResult, len(config.Targets))
	var waitGroup sync.WaitGroup
	for index, target := range config.Targets {
		waitGroup.Add(1)
		go func(index int, target TargetSpec) {
			defer waitGroup.Done()
			results[index] = coordinator.safeCheckTarget(target, config.PingTimeoutMS, cycleID, fmt.Sprintf("worker-%d", index+1))
		}(index, target)
	}
	waitGroup.Wait()
	return results
}

func (coordinator *CheckCoordinator) safeCheckTarget(target TargetSpec, timeoutMS, cycleID int, workerID string) (result CheckResult) {
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
	return coordinator.checkTarget(target, timeoutMS, cycleID, workerID)
}

func (coordinator *CheckCoordinator) checkTarget(target TargetSpec, timeoutMS, cycleID int, workerID string) CheckResult {
	if target.Kind == "ip" {
		pingSuccess, latencyMS, errorCategory, errorMessage := coordinator.pingRunner.Ping(target.Value, timeoutMS)
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

	dnsSuccess, resolvedIP, dnsError := coordinator.dnsResolver.Resolve(target.Value, timeoutMS)
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

	pingSuccess, latencyMS, errorCategory, errorMessage := coordinator.pingRunner.Ping(resolvedIP, timeoutMS)
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
