package pingtop

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var durationInputRE = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([smhd]?)\s*$`)

func localTime(value time.Time) time.Time {
	if value.IsZero() {
		value = time.Now()
	}
	return value.UTC().Local()
}

func NowLocalISO(value time.Time, milliseconds bool) string {
	timestamp := localTime(value)
	if milliseconds {
		return timestamp.Format("2006-01-02T15:04:05.000-07:00")
	}
	return timestamp.Format("2006-01-02T15:04:05-07:00")
}

func clampFloat(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(maximum, value))
}

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func FormatDuration(seconds float64) string {
	if seconds >= 60 {
		minutes := int(seconds) / 60
		remainder := int(seconds) % 60
		return fmt.Sprintf("%dm%02ds", minutes, remainder)
	}
	if seconds >= 10 {
		return fmt.Sprintf("%.0fs", seconds)
	}
	return fmt.Sprintf("%.1fs", seconds)
}

func FormatCompactSpan(seconds int) string {
	if seconds <= 0 {
		return "0s"
	}
	units := []struct {
		suffix string
		size   int
	}{
		{"d", 86400},
		{"h", 3600},
		{"m", 60},
		{"s", 1},
	}
	remaining := seconds
	parts := make([]string, 0, 2)
	for _, unit := range units {
		if remaining >= unit.size {
			value := remaining / unit.size
			remaining = remaining % unit.size
			parts = append(parts, fmt.Sprintf("%d%s", value, unit.suffix))
		}
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%ds", seconds)
	}
	return strings.Join(parts, "")
}

func AbbreviateCount(value int) string {
	absolute := math.Abs(float64(value))
	if absolute < 1000 {
		return strconv.Itoa(value)
	}
	units := []struct {
		divisor float64
		suffix  string
	}{
		{1_000_000_000, "b"},
		{1_000_000, "m"},
		{1_000, "k"},
	}
	for _, unit := range units {
		if absolute >= unit.divisor {
			scaled := float64(value) / unit.divisor
			switch {
			case math.Abs(scaled) >= 100:
				return fmt.Sprintf("%.0f%s", scaled, unit.suffix)
			case math.Abs(scaled) >= 10:
				return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", scaled), "0"), ".") + unit.suffix
			default:
				return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", scaled), "0"), ".") + unit.suffix
			}
		}
	}
	return strconv.Itoa(value)
}

func AbbreviateRatio(left, right int) string {
	return AbbreviateCount(left) + "/" + AbbreviateCount(right)
}

func ParseDurationInput(raw string) (int, error) {
	match := durationInputRE.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return 0, errors.New("expected a number with optional s/m/h/d suffix")
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, err
	}
	unit := strings.ToLower(match[2])
	if unit == "" {
		unit = "s"
	}
	multiplier := map[string]float64{
		"s": 1,
		"m": 60,
		"h": 3600,
		"d": 86400,
	}[unit]
	seconds := int(value * multiplier)
	if seconds <= 0 {
		return 0, errors.New("duration must be greater than zero")
	}
	return seconds, nil
}

func FormatLatency(latencyMS *float64) string {
	if latencyMS == nil {
		return "-"
	}
	value := *latencyMS
	if value >= 1000 {
		return fmt.Sprintf("%.2fs", value/1000.0)
	}
	if value >= 100 {
		return fmt.Sprintf("%.0fms", value)
	}
	return fmt.Sprintf("%.1fms", value)
}

func FormatTimestampShort(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return localTime(value).Format("15:04:05")
}

func Shorten(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(text) <= width {
		return text
	}
	if width <= 3 {
		return text[:width]
	}
	return text[:width-3] + "..."
}

func HumanErrorMessage(result CheckResult) string {
	if result.ErrorMessage != "" {
		return result.ErrorMessage
	}
	if result.ErrorCategory == "ok" {
		return "ok"
	}
	return strings.ReplaceAll(result.ErrorCategory, "_", " ")
}

func cloneLatency(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func IsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func DefaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
