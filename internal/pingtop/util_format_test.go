package pingtop

import (
	"math"
	"testing"
)

func TestFormatLossPercentageKeepsSmallLossVisible(t *testing.T) {
	for _, test := range []struct {
		name       string
		percentage float64
		want       string
	}{
		{name: "zero", percentage: 0, want: "0.0%"},
		{name: "negative zero", percentage: math.Copysign(0, -1), want: "0.0%"},
		{name: "smallest positive", percentage: math.SmallestNonzeroFloat64, want: "<0.1%"},
		{name: "one failure in 6911 checks", percentage: 100.0 / 6911, want: "<0.1%"},
		{name: "two failures in 9280 checks", percentage: 200.0 / 9280, want: "<0.1%"},
		{name: "just below threshold", percentage: 0.0999, want: "<0.1%"},
		{name: "threshold", percentage: 0.1, want: "0.1%"},
		{name: "ordinary loss", percentage: 12.34, want: "12.3%"},
		{name: "all checks failed", percentage: 100, want: "100.0%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatLossPercentage(test.percentage); got != test.want {
				t.Fatalf("FormatLossPercentage(%g) = %q; want %q", test.percentage, got, test.want)
			}
		})
	}
}

func TestFormatLatencyShowsSubMillisecondPrecision(t *testing.T) {
	if got := FormatLatency(nil); got != "-" {
		t.Fatalf("FormatLatency(nil) = %q; want '-'", got)
	}
	for _, test := range []struct {
		name  string
		value float64
		want  string
	}{
		{name: "zero millisecond native reply", value: 0, want: "<1ms"},
		{name: "submillisecond reply", value: 0.9, want: "<1ms"},
		{name: "just below one millisecond", value: math.Nextafter(1, 0), want: "<1ms"},
		{name: "one millisecond", value: 1, want: "1.0ms"},
		{name: "fractional milliseconds", value: 12.3, want: "12.3ms"},
		{name: "hundred milliseconds", value: 100, want: "100ms"},
		{name: "one second", value: 1000, want: "1.00s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := test.value
			if got := FormatLatency(&value); got != test.want {
				t.Fatalf("FormatLatency(%g) = %q; want %q", test.value, got, test.want)
			}
			if value != test.value {
				t.Fatalf("formatting changed measured latency from %g to %g", test.value, value)
			}
		})
	}
}
