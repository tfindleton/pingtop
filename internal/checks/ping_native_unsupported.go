//go:build !windows

package checks

func nativePing(ipAddress string, timeoutMS int) (bool, *float64, string, string, bool) {
	return false, nil, "", "", false
}
