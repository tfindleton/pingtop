package checks

import (
	"context"
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
