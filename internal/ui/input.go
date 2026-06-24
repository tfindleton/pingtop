package ui

import "time"

type InputHandler interface {
	ReadKeys(timeout time.Duration) []string
	Close() error
}
