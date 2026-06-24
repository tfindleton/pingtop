//go:build linux

package ui

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func TerminalSize() (int, int) {
	ws := &winsize{}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)),
	)
	if errno == 0 && ws.Col > 0 && ws.Row > 0 {
		return int(ws.Col), int(ws.Row)
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
	return pingtop.IsTerminal(file)
}

func enableANSI(file *os.File) bool {
	return SupportsTTY(file)
}
