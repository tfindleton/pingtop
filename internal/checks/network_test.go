package checks

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPingRunnerBuildsWindowsLinuxAndDarwinCommands(t *testing.T) {
	runner := NewPingRunner()
	runner.goos = "windows"
	windows := runner.buildCommand("1.1.1.1", 1200)
	expectedWindows := []string{"ping", "-n", "1", "-w", "1200", "1.1.1.1"}
	if !equalStrings(windows, expectedWindows) {
		t.Fatalf("unexpected windows command: %#v", windows)
	}

	runner.goos = "linux"
	linux := runner.buildCommand("1.1.1.1", 1200)
	expectedLinux := []string{"ping", "-n", "-c", "1", "-W", "2", "1.1.1.1"}
	if !equalStrings(linux, expectedLinux) {
		t.Fatalf("unexpected linux command: %#v", linux)
	}

	runner.goos = "darwin"
	darwin := runner.buildCommand("1.1.1.1", 1200)
	expectedDarwin := []string{"ping", "-n", "-c", "1", "-W", "1200", "1.1.1.1"}
	if !equalStrings(darwin, expectedDarwin) {
		t.Fatalf("unexpected darwin command: %#v", darwin)
	}
}

func TestDarwinIPv6UsesPing6WithConfiguredWait(t *testing.T) {
	runner := &PingRunner{goos: "darwin", pingPath: "/sbin/ping", ping6Path: "/sbin/ping6"}
	for _, test := range []struct {
		timeoutMS int
		interval  string
	}{
		{timeoutMS: 250, interval: "1"},
		{timeoutMS: 1200, interval: "2"},
		{timeoutMS: 30000, interval: "30"},
	} {
		t.Run(fmt.Sprintf("timeout=%dms", test.timeoutMS), func(t *testing.T) {
			command := runner.buildCommand("::1", test.timeoutMS)
			expected := []string{"ping6", "-n", "-c", "1", "-i", test.interval, "::1"}
			if !equalStrings(command, expected) {
				t.Fatalf("expected one IPv6 probe with wait covering configured deadline: %#v", command)
			}
		})
	}
	if got := runner.commandPath("ping6"); got != "/sbin/ping6" {
		t.Fatalf("expected IPv6 executable, got %q", got)
	}
	runner.ping6Path = ""
	if got := runner.commandPath("ping6"); got != "ping6" {
		t.Fatalf("expected missing IPv6 executable to use PATH lookup, got %q", got)
	}
}

func TestDarwinIPv6Loopback(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS ping6")
	}
	runner := NewPingRunner()
	defer runner.Close()
	ok, latency, category, message := runner.Ping("::1", 1000)
	if !ok || latency == nil {
		t.Fatalf("expected IPv6 loopback reply: ok=%v latency=%v category=%q message=%q", ok, latency, category, message)
	}
}

func TestDNSResolverTimesOutEachLookupWithoutReusingStalePending(t *testing.T) {
	var calls int32
	resolver := NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(200 * time.Millisecond)
		return false, "", "slow failure"
	})

	ok, _, errMessage := resolver.Resolve("hhh", 50)
	if ok || !strings.Contains(errMessage, "exceeded 50 ms timeout") {
		t.Fatalf("unexpected first resolve result: ok=%v err=%q", ok, errMessage)
	}
	ok, _, errMessage = resolver.Resolve("hhh", 50)
	if ok || !strings.Contains(errMessage, "exceeded 50 ms timeout") {
		t.Fatalf("unexpected second resolve result: ok=%v err=%q", ok, errMessage)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected each timed-out resolve to start a fresh lookup, got %d calls", got)
	}
}

func TestDNSResolverUsesContextCancellation(t *testing.T) {
	resolver := NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		<-ctx.Done()
		return false, "", ctx.Err().Error()
	})

	ok, _, errMessage := resolver.Resolve("hhh", 50)
	if ok || !strings.Contains(errMessage, "exceeded 50 ms timeout") {
		t.Fatalf("unexpected resolve result: ok=%v err=%q", ok, errMessage)
	}
}

func TestDNSResolverPreservesParentCancellation(t *testing.T) {
	for _, cancelBeforeLookup := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancelBeforeLookup=%v", cancelBeforeLookup), func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			resolver := NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
				close(started)
				<-release
				return false, "", "lookup stopped"
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelBeforeLookup {
				cancel()
			} else {
				go func() {
					<-started
					cancel()
				}()
			}
			ok, address, message := resolver.ResolveContext(ctx, "example.test", 1000)
			if ok || address != "" || message != context.Canceled.Error() {
				t.Fatalf("expected cancellation, got ok=%v address=%q message=%q", ok, address, message)
			}
		})
	}
}

func TestCheckTargetCanceledDuringDNSDoesNotReportFailure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	resolver := NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		close(started)
		<-release
		return false, "", "lookup stopped"
	})
	coordinator := NewCheckCoordinator(NewPingRunner(), resolver)
	defer coordinator.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	result := coordinator.CheckTargetContext(ctx, TargetSpec{Value: "example.test", Kind: "hostname"}, 1000, 1, "worker-1")
	if result.ErrorCategory != "canceled" {
		t.Fatalf("expected cancellation rather than a failed check, got %#v", result)
	}
}

func TestExecuteCycleContextReturnsPromptlyWhenCanceled(t *testing.T) {
	resolver := NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		<-ctx.Done()
		return false, "", ctx.Err().Error()
	})
	coordinator := NewCheckCoordinator(NewPingRunner(), resolver)
	defer coordinator.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	results := coordinator.ExecuteCycleContext(ctx, AppConfig{
		PingTimeoutMS: 1200,
		Targets:       []TargetSpec{{Value: "slow.example", Kind: "hostname"}},
	}, 1, nil)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("expected canceled cycle to return promptly, took %s", elapsed)
	}
	if results != nil {
		t.Fatalf("expected canceled cycle to return nil results, got %#v", results)
	}
}

func TestExecuteCycleContextKeepsCompletedResultsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	releaseFirst := make(chan struct{})
	resolver := NewDNSResolver(func(ctx context.Context, hostname string) (bool, string, string) {
		switch hostname {
		case "first.example":
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		case "slow.example":
			<-ctx.Done()
		}
		return false, "", "lookup failed"
	})
	coordinator := NewCheckCoordinator(NewPingRunner(), resolver)
	defer coordinator.Close()
	results := coordinator.ExecuteCycleContext(ctx, AppConfig{
		PingTimeoutMS: 10000,
		Targets: []TargetSpec{
			{Value: "first.example", Kind: "hostname"},
			{Value: "slow.example", Kind: "hostname"},
			{Value: "last.example", Kind: "hostname"},
		},
	}, 7, func(result CheckResult) {
		if result.Target == "last.example" {
			close(releaseFirst)
		}
		if result.Target == "first.example" {
			cancel()
		}
	})
	if len(results) != 2 || results[0].Target != "first.example" || results[1].Target != "last.example" {
		t.Fatalf("expected completed results in configured target order, got %#v", results)
	}
	for _, result := range results {
		if result.CycleID != 7 || result.ErrorCategory != "dns_failure" {
			t.Fatalf("completed failure was replaced with cancellation or an empty result: %#v", result)
		}
	}
}

func TestExecuteCycleContextSkipsDisabledTargets(t *testing.T) {
	coordinator := NewCheckCoordinator(NewPingRunner(), nil)
	defer coordinator.Close()

	results := coordinator.ExecuteCycleContext(context.Background(), AppConfig{
		PingTimeoutMS: 1200,
		Targets: []TargetSpec{
			{Value: "disabled.example", Kind: "hostname", Disabled: true},
		},
	}, 1, nil)
	if results != nil {
		t.Fatalf("expected disabled-only cycle to return nil results, got %#v", results)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
