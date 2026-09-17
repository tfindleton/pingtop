//go:build !windows

package checks

import "context"

func nativePingContext(ctx context.Context, ipAddress string, timeoutMS int) (bool, *float64, string, string, bool) {
	return false, nil, "", "", false
}
