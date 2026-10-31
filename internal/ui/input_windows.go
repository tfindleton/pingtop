//go:build windows

package ui

import (
	"fmt"
	"os"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxWindowsInputBatch = 64

var procReadConsoleInput = modKernel32.NewProc("ReadConsoleInputW")

// INPUT_RECORD with the KEY_EVENT_RECORD union member. The union starts at a
// DWORD boundary; other event types are consumed but not interpreted as keys.
type consoleInputRecord struct {
	eventType       uint16
	padding         uint16
	keyDown         int32
	repeatCount     uint16
	virtualKeyCode  uint16
	virtualScanCode uint16
	character       uint16
	controlKeyState uint32
}

type windowsInputHandler struct {
	input         windows.Handle
	originalMode  uint32
	closed        bool
	buffer        [maxWindowsInputBatch]consoleInputRecord
	pending       []consoleInputRecord
	repeatKey     string
	repeats       uint16
	highSurrogate uint16
}

func InteractiveSupported() bool {
	return true
}

func NewInputHandler() (InputHandler, error) {
	if os.Stdin == nil {
		return nil, fmt.Errorf("standard input is unavailable")
	}
	input := windows.Handle(os.Stdin.Fd())
	var original uint32
	if err := windows.GetConsoleMode(input, &original); err != nil {
		return nil, err
	}
	if err := procReadConsoleInput.Find(); err != nil {
		return nil, err
	}
	// Do not inherit a shell's VT input translation, line buffering, or Quick
	// Edit selection behavior. We read key events directly, including Ctrl+C.
	mode := original | windows.ENABLE_EXTENDED_FLAGS | windows.ENABLE_WINDOW_INPUT
	mode &^= windows.ENABLE_QUICK_EDIT_MODE | windows.ENABLE_MOUSE_INPUT |
		windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT |
		windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(input, mode); err != nil {
		return nil, err
	}
	return &windowsInputHandler{input: input, originalMode: original}, nil
}

func (handler *windowsInputHandler) ReadKeys(timeout time.Duration) []string {
	if handler.closed {
		return nil
	}
	if keys := handler.takePendingKeys(); len(keys) > 0 {
		return keys
	}
	waitMS := max(int64(0), int64(timeout/time.Millisecond))
	if timeout > 0 && timeout%time.Millisecond != 0 {
		waitMS++
	}
	waitMS = min(waitMS, int64(windows.INFINITE)-1)
	status, err := windows.WaitForSingleObject(handler.input, uint32(waitMS))
	if err != nil || status != windows.WAIT_OBJECT_0 {
		return nil
	}
	// ReadConsoleInput blocks on an empty queue. Check availability first and
	// consume at most one batch, including focus, resize, and mouse events.
	// CRT _kbhit only peeked at those events, repeatedly scanning stale input.
	var available uint32
	if err := windows.GetNumberOfConsoleInputEvents(handler.input, &available); err != nil || available == 0 {
		return nil
	}
	var read uint32
	ok, _, _ := procReadConsoleInput.Call(uintptr(handler.input), uintptr(unsafe.Pointer(&handler.buffer[0])),
		uintptr(min(available, uint32(len(handler.buffer)))), uintptr(unsafe.Pointer(&read)))
	if ok == 0 {
		return nil
	}
	handler.pending = handler.buffer[:read]
	return handler.takePendingKeys()
}

func (handler *windowsInputHandler) takePendingKeys() []string {
	var keys []string
	for len(keys) < maxWindowsInputBatch {
		if handler.repeats > 0 {
			keys = append(keys, handler.repeatKey)
			handler.repeats--
			continue
		}
		if len(handler.pending) == 0 {
			break
		}
		record := handler.pending[0]
		handler.pending = handler.pending[1:]
		if record.eventType != 1 || record.keyDown == 0 || record.repeatCount == 0 {
			continue
		}
		if key := handler.decodeKey(record); key != "" {
			handler.repeatKey = key
			handler.repeats = record.repeatCount
		}
	}
	return keys
}

func (handler *windowsInputHandler) decodeKey(record consoleInputRecord) string {
	var special string
	switch record.virtualKeyCode {
	case 0x1b:
		special = KeyEscape
	case 0x21:
		special = KeyPageUp
	case 0x22:
		special = KeyPageDown
	case 0x25:
		special = KeyLeft
	case 0x26:
		special = KeyUp
	case 0x27:
		special = KeyRight
	case 0x28:
		special = KeyDown
	}
	if special != "" {
		handler.highSurrogate = 0
		return special
	}
	char := record.character
	if char == 0 {
		return ""
	}
	if char >= 0xd800 && char <= 0xdbff {
		handler.highSurrogate = char
		return ""
	}
	high := handler.highSurrogate
	handler.highSurrogate = 0
	if char >= 0xdc00 && char <= 0xdfff {
		return string(utf16.DecodeRune(rune(high), rune(char)))
	}
	if char == '\x1b' {
		return KeyEscape
	}
	return string(rune(char))
}

func (handler *windowsInputHandler) Close() error {
	if handler.closed {
		return nil
	}
	handler.closed = true
	return windows.SetConsoleMode(handler.input, handler.originalMode)
}

func FormatInputError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("interactive input unavailable: %v", err)
}
