package ui

import (
	"reflect"
	"testing"
)

func TestDecodeInputBytesParsesArrowsAndPaging(t *testing.T) {
	raw := []byte{
		0x1b, '[', 'A',
		0x1b, '[', 'B',
		0x1b, '[', '5', '~',
		0x1b, '[', '6', '~',
	}

	got := decodeInputBytes(raw)
	want := []string{KeyUp, KeyDown, KeyPageUp, KeyPageDown}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestDecodeInputBytesKeepsBareEscapeDistinct(t *testing.T) {
	got := decodeInputBytes([]byte{0x1b})
	want := []string{KeyEscape}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestTrailingEscapeSequenceNeedsMore(t *testing.T) {
	if !trailingEscapeSequenceNeedsMore([]byte{0x1b, '['}) {
		t.Fatal("expected partial escape sequence to request more input")
	}
	if trailingEscapeSequenceNeedsMore([]byte{0x1b, '[', 'A'}) {
		t.Fatal("expected complete arrow sequence to stop waiting")
	}
}

func TestSpecialWindowsKeyMapsScrollKeys(t *testing.T) {
	tests := map[rune]string{
		72: KeyUp,
		80: KeyDown,
		73: KeyPageUp,
		81: KeyPageDown,
	}
	for scanCode, want := range tests {
		if got := specialWindowsKey(scanCode); got != want {
			t.Fatalf("scan code %d: expected %q, got %q", scanCode, want, got)
		}
	}
}
