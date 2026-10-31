package app

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

type scriptedInput struct {
	t       *testing.T
	batches [][]string
}

func (input *scriptedInput) ReadKeys(time.Duration) []string {
	input.t.Helper()
	if len(input.batches) == 0 {
		input.t.Fatal("input loop did not stop after quit")
	}
	batch := input.batches[0]
	input.batches = input.batches[1:]
	return batch
}

func (*scriptedInput) Close() error { return nil }

func captureInputLoop(t *testing.T, ui *PingTopUI, batches ...[]string) string {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "screen-")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = output
	defer func() {
		os.Stdout = previous
		output.Close()
	}()
	ui.runInputLoop(&scriptedInput{t: t, batches: batches})
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInputLoopRendersOncePerBatch(t *testing.T) {
	ui := newReviewTestUI(t)
	keys := []string{"a"}
	for _, char := range "example.com" {
		keys = append(keys, string(char))
	}
	output := captureInputLoop(t, ui, keys, []string{"\x03"})
	if ui.prompt == nil || ui.prompt.Buffer != "example.com" {
		t.Fatalf("input batch lost characters: %#v", ui.prompt)
	}
	if frames := strings.Count(output, "pingtop "+pingtop.Version); frames != 2 {
		t.Fatalf("expected initial frame and one completed input batch, got %d frames", frames)
	}
	if !strings.Contains(output, "example.com") {
		t.Fatal("completed prompt was not rendered before waiting for more input")
	}
}

func TestInputLoopStopsProcessingKeysAfterQuit(t *testing.T) {
	ui := newReviewTestUI(t)
	captureInputLoop(t, ui, []string{"q", "p"})
	if ui.running || ui.monitor.IsPaused() {
		t.Fatal("keys after quit were still processed")
	}
}
