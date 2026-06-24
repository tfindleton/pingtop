//go:build !linux && !windows

package ui

import (
	"errors"
	"time"
)

type unsupportedInputHandler struct{}

func InteractiveSupported() bool {
	return false
}

func NewInputHandler() (InputHandler, error) {
	return nil, errors.New("interactive input is only implemented for Linux/WSL and Windows in this build")
}

func (handler *unsupportedInputHandler) ReadKeys(timeout time.Duration) []string {
	return nil
}

func (handler *unsupportedInputHandler) Close() error {
	return nil
}

func FormatInputError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
