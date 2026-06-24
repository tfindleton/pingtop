//go:build windows

package ui

import (
	"fmt"
	"syscall"
	"time"
)

var (
	modMSVCRT  = syscall.NewLazyDLL("msvcrt.dll")
	procKbhit  = modMSVCRT.NewProc("_kbhit")
	procGetWCh = modMSVCRT.NewProc("_getwch")
)

type windowsInputHandler struct{}

func InteractiveSupported() bool {
	return true
}

func NewInputHandler() (InputHandler, error) {
	if err := modMSVCRT.Load(); err != nil {
		return nil, err
	}
	if err := procKbhit.Find(); err != nil {
		return nil, err
	}
	if err := procGetWCh.Find(); err != nil {
		return nil, err
	}
	return &windowsInputHandler{}, nil
}

func (handler *windowsInputHandler) ReadKeys(timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	keys := make([]string, 0, 8)
	for {
		for {
			ready, _, _ := procKbhit.Call()
			if ready == 0 {
				break
			}
			value, _, _ := procGetWCh.Call()
			key := rune(value)
			if key == 0 || key == 0xe0 {
				scanCode, _, _ := procGetWCh.Call()
				if special := specialWindowsKey(rune(scanCode)); special != "" {
					keys = append(keys, special)
				}
				continue
			}
			if key == '\x1b' {
				keys = append(keys, KeyEscape)
				continue
			}
			keys = append(keys, string(key))
		}
		if len(keys) > 0 || !time.Now().Before(deadline) {
			return keys
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (handler *windowsInputHandler) Close() error {
	return nil
}

func FormatInputError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("interactive input unavailable: %v", err)
}
