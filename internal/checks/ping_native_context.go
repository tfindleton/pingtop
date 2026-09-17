package checks

import (
	"context"
	"fmt"
)

type nativePingResult struct {
	success       bool
	latencyMS     *float64
	errorCategory string
	errorMessage  string
	handled       bool
}

func runNativePingContext(ctx context.Context, probe func() (bool, *float64, string, string, bool)) (bool, *float64, string, string, bool) {
	if err := ctx.Err(); err != nil {
		return false, nil, "canceled", err.Error(), true
	}

	// A synchronous native probe owns its handle and buffers until its bounded
	// timeout completes. Cancellation releases the caller immediately; the
	// buffered result lets the probe finish and clean up without a receiver.
	resultCh := make(chan nativePingResult, 1)
	go func() {
		var result nativePingResult
		defer func() {
			if recovered := recover(); recovered != nil {
				result = nativePingResult{
					errorCategory: "internal_error",
					errorMessage:  shorten(fmt.Sprint(recovered), 180),
					handled:       true,
				}
			}
			resultCh <- result
		}()
		result.success, result.latencyMS, result.errorCategory, result.errorMessage, result.handled = probe()
	}()

	select {
	case <-ctx.Done():
		return false, nil, "canceled", ctx.Err().Error(), true
	case result := <-resultCh:
		if err := ctx.Err(); err != nil {
			return false, nil, "canceled", err.Error(), true
		}
		return result.success, result.latencyMS, result.errorCategory, result.errorMessage, result.handled
	}
}
