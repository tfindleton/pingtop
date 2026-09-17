//go:build windows

package checks

import (
	"context"
	"syscall"
	"testing"
	"unsafe"
)

func TestNativePingLoopback(t *testing.T) {
	ok, latencyMS, category, message, handled := nativePingContext(context.Background(), "127.0.0.1", 1000)
	if !handled {
		t.Fatal("expected native ping fast path to handle IPv4 loopback")
	}
	if !ok {
		t.Fatalf("expected loopback ping to succeed, category=%q message=%q", category, message)
	}
	if latencyMS == nil {
		t.Fatal("expected native ping latency to be populated")
	}
}

func TestNativePingPreservesFailedProbesWithoutRetry(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		category string
	}{
		{name: "timeout", err: syscall.Errno(ipReqTimedOut), category: "timeout"},
		{name: "host unreachable", err: syscall.Errno(11003), category: "ping_failure"},
		{name: "native API failure", err: syscall.EINVAL, category: "ping_failure"},
		{name: "no error supplied", err: syscall.Errno(0), category: "ping_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ok, latency, category, message, handled := parseICMPReply(0, nil, test.err)
			if !handled || ok || latency != nil || category != test.category || message == "" {
				t.Fatalf("expected native failure without fallback: ok=%v latency=%v category=%q message=%q handled=%v", ok, latency, category, message, handled)
			}
		})
	}
}

func TestNativePingDistinguishesEchoRepliesFromICMPErrors(t *testing.T) {
	for _, status := range []uint32{ipSuccess, ipReqTimedOut, 11003} {
		buffer := make([]byte, unsafe.Sizeof(icmpEchoReply{}))
		reply := (*icmpEchoReply)(unsafe.Pointer(&buffer[0]))
		reply.Status = status
		reply.RoundTripTime = 7
		ok, latency, category, _, handled := parseICMPReply(1, buffer, syscall.Errno(0))
		if !handled || ok != (status == ipSuccess) {
			t.Fatalf("unexpected result for ICMP status %d: ok=%v category=%q handled=%v", status, ok, category, handled)
		}
		if ok && (latency == nil || *latency != 7) {
			t.Fatalf("expected 7ms echo latency, got %v", latency)
		}
		if !ok && latency != nil {
			t.Fatalf("expected no latency on failure, got %v", latency)
		}
	}
}
