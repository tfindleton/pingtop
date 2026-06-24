//go:build !linux && !windows

package ui

import (
	"os"
	"strconv"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

func TerminalSize() (int, int) {
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
