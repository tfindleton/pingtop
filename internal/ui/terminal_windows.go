//go:build windows

package ui

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

const enableVirtualTerminalProcessing = 0x0004

type coord struct {
	X int16
	Y int16
}

type smallRect struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

var (
	modKernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = modKernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = modKernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = modKernel32.NewProc("GetConsoleScreenBufferInfo")
)

func TerminalSize() (int, int) {
	if os.Stdout != nil {
		info := consoleScreenBufferInfo{}
		result, _, _ := procGetConsoleScreenBufferInfo.Call(
			os.Stdout.Fd(),
			uintptr(unsafe.Pointer(&info)),
		)
		if result != 0 {
			width := int(info.Window.Right-info.Window.Left) + 1
			height := int(info.Window.Bottom-info.Window.Top) + 1
			if width > 0 && height > 0 {
				return width, height
			}
		}
	}
	return terminalSizeFromEnv(140, 42)
}

func terminalSizeFromEnv(defaultWidth, defaultHeight int) (int, int) {
	width := defaultWidth
	height := defaultHeight
	if value, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && value > 0 {
		width = value
	}
	if value, err := strconv.Atoi(os.Getenv("LINES")); err == nil && value > 0 {
		height = value
	}
	return width, height
}

func SupportsTTY(file *os.File) bool {
	if file == nil {
		return false
	}
	mode := uint32(0)
	result, _, _ := procGetConsoleMode.Call(
		file.Fd(),
		uintptr(unsafe.Pointer(&mode)),
	)
	return result != 0
}

func enableANSI(file *os.File) bool {
	if !SupportsTTY(file) {
		return false
	}
	mode := uint32(0)
	result, _, _ := procGetConsoleMode.Call(
		file.Fd(),
		uintptr(unsafe.Pointer(&mode)),
	)
	if result == 0 {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	result, _, _ = procSetConsoleMode.Call(
		file.Fd(),
		uintptr(mode|enableVirtualTerminalProcessing),
	)
	return result != 0
}
