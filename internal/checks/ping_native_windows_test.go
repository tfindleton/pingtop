//go:build windows

package checks

import "testing"

func TestNativePingLoopback(t *testing.T) {
	ok, latencyMS, category, message, handled := nativePing("127.0.0.1", 1000)
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
