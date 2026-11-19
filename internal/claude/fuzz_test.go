package claude

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// FuzzLines feeds arbitrary input through every Sink and the session
// summary: nothing may panic, and every non-blank line is counted.
func FuzzLines(f *testing.F) {
	for _, path := range []string{transcriptOne(), transcriptTwo()} {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{"type":"assistant","message":{"id":"m","content":"x","usage":{"input_tokens":-1}}}` + "\n"))
	f.Add([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":7}]}]}}` + "\n"))
	f.Add([]byte("{\"type\":\"ai-title\",\"aiTitle\":\"\x1b[2J\"}\n\xff\xfe\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		acc := NewAccumulator()
		var conv Conversation
		var off int64
		want := 0
		for _, l := range bytes.SplitAfter(data, []byte("\n")) {
			if len(l) == 0 {
				continue
			}
			if len(bytes.TrimSpace(l)) > 0 {
				want++
			}
			acc.Line(off, l)
			conv.Line(off, l)
			off += int64(len(l))
		}
		st := acc.State()
		if st.Lines != want || st.Skipped > st.Lines || st.Offset != off {
			t.Fatalf("lines %d skipped %d offset %d; want %d lines, offset %d", st.Lines, st.Skipped, st.Offset, want, off)
		}
		_ = acc.Session(SessionInput{ID: "s"})
		_ = conv.Entries(0)
		if _, err := RestoreAccumulator(st, bytes.NewReader(data)); err != nil && !errors.Is(err, ErrStaleState) {
			t.Fatalf("restore: %v", err)
		}
	})
}
