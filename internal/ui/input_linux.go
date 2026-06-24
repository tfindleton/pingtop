//go:build linux

package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const inputSequenceTimeout = 10 * time.Millisecond

type linuxInputHandler struct {
	fd           int
	originalMode string
}

func InteractiveSupported() bool {
	return true
}

func NewInputHandler() (InputHandler, error) {
	getMode := exec.Command("stty", "-g")
	getMode.Stdin = os.Stdin
	output, err := getMode.Output()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("stty", "raw", "-echo")
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return &linuxInputHandler{
		fd:           int(os.Stdin.Fd()),
		originalMode: strings.TrimSpace(string(output)),
	}, nil
}

func (handler *linuxInputHandler) Close() error {
	if handler.originalMode == "" {
		return nil
	}
	cmd := exec.Command("stty", handler.originalMode)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (handler *linuxInputHandler) ReadKeys(timeout time.Duration) []string {
	if !handler.waitReadable(timeout) {
		return nil
	}
	buffer := make([]byte, 64)
	raw := make([]byte, 0, 16)
	for {
		count, err := syscall.Read(handler.fd, buffer)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || count <= 0 {
			break
		}
		raw = append(raw, buffer[:count]...)
		nextTimeout := time.Duration(0)
		if trailingEscapeSequenceNeedsMore(raw) {
			nextTimeout = inputSequenceTimeout
		}
		if !handler.waitReadable(nextTimeout) {
			break
		}
	}
	return decodeInputBytes(raw)
}

func (handler *linuxInputHandler) waitReadable(timeout time.Duration) bool {
	var readSet syscall.FdSet
	readSet.Bits[handler.fd/64] |= 1 << (uint(handler.fd) % 64)
	timeval := syscall.NsecToTimeval(timeout.Nanoseconds())
	count, err := syscall.Select(handler.fd+1, &readSet, nil, nil, &timeval)
	if err != nil {
		return false
	}
	if count == 0 {
		return false
	}
	return (readSet.Bits[handler.fd/64] & (1 << (uint(handler.fd) % 64))) != 0
}

func FormatInputError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("interactive input unavailable: %v", err)
}
