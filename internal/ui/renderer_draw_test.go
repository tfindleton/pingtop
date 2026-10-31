package ui

import (
	"os"
	"strings"
	"testing"
)

type frameWriter struct {
	text   strings.Builder
	writes int
}

func (writer *frameWriter) Write(data []byte) (int, error) {
	writer.writes++
	return writer.text.Write(data)
}

func TestDrawWritesOneCompleteFrame(t *testing.T) {
	previousSize := terminalSize
	terminalSize = func() (int, int) { return 80, 3 }
	t.Cleanup(func() { terminalSize = previousSize })

	renderer := &Renderer{ansi: true}
	output := &frameWriter{}
	renderer.drawTo(output, "one\ntwo\nthree")
	want := "\x1b[1;1H\x1b[2Kone\x1b[2;1H\x1b[2Ktwo\x1b[3;1H\x1b[2Kthree"
	if output.writes != 1 || output.text.String() != want {
		t.Fatalf("frame was split or changed: writes=%d text=%q", output.writes, output.text.String())
	}

	output = &frameWriter{}
	renderer.drawTo(output, "short")
	want = "\x1b[1;1H\x1b[2Kshort\x1b[2;1H\x1b[2K\x1b[3;1H\x1b[2K\x1b[2;1H\x1b[J"
	if output.writes != 1 || output.text.String() != want {
		t.Fatalf("shrinking frame left stale rows: writes=%d text=%q", output.writes, output.text.String())
	}
}

func TestDrawPlainTextKeepsExactlyOneTrailingNewline(t *testing.T) {
	for _, text := range []string{"one\ntwo", "one\ntwo\n"} {
		output := &frameWriter{}
		(&Renderer{}).drawTo(output, text)
		if output.writes != 1 || output.text.String() != "one\ntwo\n" {
			t.Fatalf("unexpected plain frame: writes=%d text=%q", output.writes, output.text.String())
		}
	}
}

func BenchmarkRendererDraw(b *testing.B) {
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	previousOutput, previousSize := os.Stdout, terminalSize
	os.Stdout = output
	terminalSize = func() (int, int) { return 160, 60 }
	b.Cleanup(func() {
		os.Stdout, terminalSize = previousOutput, previousSize
		output.Close()
	})
	frame := strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 140)+"\n", 50), "\n")
	renderer := &Renderer{ansi: true}
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderer.Draw(frame)
	}
}
