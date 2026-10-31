//go:build windows

package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// INPUT_RECORD with its KEY_EVENT_RECORD union member, including the WORD
// padding before the union. The native structure is 20 bytes on both Windows
// architectures we support.
type testConsoleInputRecord struct {
	eventType       uint16
	padding         uint16
	keyDown         int32
	repeatCount     uint16
	virtualKeyCode  uint16
	virtualScanCode uint16
	character       uint16
	controlKeyState uint32
}

func TestWindowsConsoleInput(t *testing.T) {
	if os.Getenv("PINGTOP_TEST_CONSOLE_INPUT") != "1" {
		// Use a separate, hidden console so injected input and mode changes
		// cannot interfere with the user's terminal or other tests.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsConsoleInput$", "-test.v", "-test.timeout=15s")
		cmd.Env = append(os.Environ(), "PINGTOP_TEST_CONSOLE_INPUT=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Windows console input tests failed: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}

	name, err := windows.UTF16PtrFromString("CONIN$")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	input := os.NewFile(uintptr(handle), "CONIN$")
	previousStdin := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = previousStdin
		input.Close()
	})
	flush := modKernel32.NewProc("FlushConsoleInputBuffer")
	write := modKernel32.NewProc("WriteConsoleInputW")
	writeRecords := func(records []testConsoleInputRecord) error {
		var written uint32
		ok, _, err := write.Call(uintptr(handle), uintptr(unsafe.Pointer(&records[0])),
			uintptr(len(records)), uintptr(unsafe.Pointer(&written)))
		if ok == 0 || int(written) != len(records) {
			return fmt.Errorf("write input records: written=%d error=%v", written, err)
		}
		return nil
	}
	queued := func() uint32 {
		t.Helper()
		var count uint32
		if err := windows.GetNumberOfConsoleInputEvents(handle, &count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	newHandler := func(t *testing.T) InputHandler {
		t.Helper()
		if ok, _, err := flush.Call(uintptr(handle)); ok == 0 {
			t.Fatal(err)
		}
		handler, err := NewInputHandler()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { handler.Close() })
		return handler
	}

	t.Run("console modes restored across runs", func(t *testing.T) {
		for _, original := range []uint32{
			windows.ENABLE_EXTENDED_FLAGS | windows.ENABLE_QUICK_EDIT_MODE | windows.ENABLE_MOUSE_INPUT |
				windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT,
			windows.ENABLE_EXTENDED_FLAGS | windows.ENABLE_VIRTUAL_TERMINAL_INPUT,
		} {
			if err := windows.SetConsoleMode(handle, original); err != nil {
				t.Fatal(err)
			}
			for run := 0; run < 2; run++ {
				handler := newHandler(t)
				var mode uint32
				if err := windows.GetConsoleMode(handle, &mode); err != nil {
					t.Fatal(err)
				}
				blockedModes := uint32(windows.ENABLE_QUICK_EDIT_MODE | windows.ENABLE_MOUSE_INPUT |
					windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT |
					windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
				if mode&blockedModes != 0 {
					t.Errorf("run %d left blocking/filtered input modes enabled: %#x", run+1, mode)
				}
				if err := handler.Close(); err != nil {
					t.Fatal(err)
				}
				if err := windows.GetConsoleMode(handle, &mode); err != nil || mode != original {
					t.Fatalf("run %d did not restore mode: got=%#x want=%#x error=%v", run+1, mode, original, err)
				}
			}
		}
	})

	t.Run("drain non-key events", func(t *testing.T) {
		handler := newHandler(t)
		records := make([]testConsoleInputRecord, 1024)
		for i := range records {
			records[i].eventType = 0x10 // FOCUS_EVENT
			records[i].keyDown = int32(i % 2)
		}
		if err := writeRecords(records); err != nil {
			t.Fatal(err)
		}
		before := queued()
		if before == 0 {
			t.Fatal("no console events were queued")
		}
		started := time.Now()
		for i := 0; i < 32 && queued() > 0; i++ {
			if keys := handler.ReadKeys(0); len(keys) != 0 {
				t.Fatalf("non-key events generated keys: %q", keys)
			}
		}
		if remaining := queued(); remaining != 0 {
			t.Fatalf("non-key events accumulate: before=%d remaining=%d", before, remaining)
		}
		t.Logf("drained %d non-key events in %s", before, time.Since(started))
	})

	t.Run("bounded key repeats and Unicode", func(t *testing.T) {
		handler := newHandler(t)
		if err := writeRecords([]testConsoleInputRecord{
			{eventType: 1, keyDown: 1, repeatCount: 200, virtualKeyCode: 'A', virtualScanCode: 0x1e, character: 'a'},
			{eventType: 1, keyDown: 1, repeatCount: 1, character: 0xe0}, // A character, not an extended-key prefix.
			{eventType: 1, keyDown: 1, repeatCount: 1, virtualKeyCode: 0x26, virtualScanCode: 0x48},
			{eventType: 1, keyDown: 0, repeatCount: 1, character: 'x'}, // Key release must not type a character.
			{eventType: 1, keyDown: 1, repeatCount: 1, character: 'q'},
		}); err != nil {
			t.Fatal(err)
		}
		var got []string
		for i := 0; i < 10; i++ {
			keys := handler.ReadKeys(0)
			if len(keys) > 64 {
				t.Fatalf("one read returned %d keys; a held key can starve rendering", len(keys))
			}
			got = append(got, keys...)
			if len(keys) == 0 && queued() == 0 {
				break
			}
		}
		want := make([]string, 200)
		for i := range want {
			want[i] = "a"
		}
		want = append(want, "à", KeyUp, "q")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("lost/reordered key events: got %d keys %q; want %d keys", len(got), got, len(want))
		}
	})

	t.Run("idle timeout", func(t *testing.T) {
		handler := newHandler(t)
		started := time.Now()
		if keys := handler.ReadKeys(20 * time.Millisecond); len(keys) != 0 {
			t.Fatalf("idle input returned keys: %q", keys)
		}
		if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
			t.Fatalf("input exceeded its timeout: %s", elapsed)
		}
	})

	t.Run("wake promptly on a key", func(t *testing.T) {
		handler := newHandler(t)
		injected := make(chan error, 1)
		go func() {
			time.Sleep(20 * time.Millisecond)
			injected <- writeRecords([]testConsoleInputRecord{{eventType: 1, keyDown: 1, repeatCount: 1, character: 'p'}})
		}()
		started := time.Now()
		deadline := started.Add(time.Second)
		var keys []string
		for len(keys) == 0 && time.Now().Before(deadline) {
			keys = handler.ReadKeys(time.Until(deadline))
		}
		if err := <-injected; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(keys, []string{"p"}) {
			t.Fatalf("key did not wake input: %q", keys)
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("key waited for the screen refresh timeout: %s", elapsed)
		} else {
			t.Logf("key woke input in %s", elapsed)
		}
	})
}

func TestWindowsKeyEvents(t *testing.T) {
	handler := &windowsInputHandler{}
	for _, test := range []struct {
		code uint16
		char uint16
		want string
	}{
		{0x1b, 0x1b, KeyEscape},
		{0x21, 0, KeyPageUp}, {0x22, 0, KeyPageDown},
		{0x25, 0, KeyLeft}, {0x26, 0, KeyUp}, {0x27, 0, KeyRight}, {0x28, 0, KeyDown},
		{0, '\x03', "\x03"}, {0, '\b', "\b"}, {0, '\r', "\r"}, {0, ' ', " "},
		{0, 0xe0, "à"}, {0, 0, ""},
		{0, 0xd83d, ""}, {0, 0xde03, "😃"},
	} {
		if key := handler.decodeKey(consoleInputRecord{virtualKeyCode: test.code, character: test.char}); key != test.want {
			t.Fatalf("code=%#x char=%#x: got %q want %q", test.code, test.char, key, test.want)
		}
	}
}
