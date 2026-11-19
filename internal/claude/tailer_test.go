package claude

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailerHoldsBackPartialLine(t *testing.T) {
	p := writeFile(t, "t.jsonl", "{\"a\":1}\n{\"b\":2}\n{\"c\":")
	tl := NewTailer(p, 0)
	var r recorder

	n, err := tl.Read(&r)
	if err != nil || n != 2 {
		t.Fatalf("first read: n=%d err=%v, want 2 lines", n, err)
	}
	if got, want := tl.Offset(), int64(len("{\"a\":1}\n{\"b\":2}\n")); got != want {
		t.Fatalf("offset = %d, want %d", got, want)
	}
	if r.offsets[1] != 8 {
		t.Errorf("second line offset = %d, want 8", r.offsets[1])
	}

	n, err = tl.Read(&r)
	if err != nil || n != 0 {
		t.Fatalf("read with only a partial line: n=%d err=%v", n, err)
	}

	appendFile(t, p, "3}\n")
	n, err = tl.Read(&r)
	if err != nil || n != 1 {
		t.Fatalf("read after completing line: n=%d err=%v", n, err)
	}
	if got := r.lines[2]; got != "{\"c\":3}\n" {
		t.Errorf("completed line = %q", got)
	}
	if r.resets != 0 {
		t.Errorf("resets = %d, want 0", r.resets)
	}
}

func TestTailerResetsWhenFileShrinks(t *testing.T) {
	p := writeFile(t, "t.jsonl", "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n")
	tl := NewTailer(p, 0)
	var r recorder
	if _, err := tl.Read(&r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{\"z\":9}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := tl.Read(&r)
	if err != nil {
		t.Fatal(err)
	}
	if r.resets != 1 || n != 1 || r.lines[0] != "{\"z\":9}\n" || tl.Offset() != 8 {
		t.Errorf("after shrink: resets=%d n=%d lines=%q offset=%d", r.resets, n, r.lines, tl.Offset())
	}
}

func TestTailerResetsWhenFileReplaced(t *testing.T) {
	// The new file is longer, so only the content check can notice it.
	p := writeFile(t, "t.jsonl", "{\"a\":1}\n")
	tl := NewTailer(p, 0)
	var r recorder
	if _, err := tl.Read(&r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{\"long\":1234}\n{\"x\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Read(&r); err != nil {
		t.Fatal(err)
	}
	if r.resets != 1 || len(r.lines) != 2 {
		t.Errorf("after replace: resets=%d lines=%q", r.resets, r.lines)
	}
}

func TestTailerResumesFromOffset(t *testing.T) {
	content := "{\"a\":1}\n{\"b\":2}\n"
	p := writeFile(t, "t.jsonl", content)
	var r recorder
	if _, err := NewTailer(p, 8).Read(&r); err != nil {
		t.Fatal(err)
	}
	if r.resets != 0 || len(r.lines) != 1 || r.offsets[0] != 8 {
		t.Errorf("resume: resets=%d lines=%q offsets=%v", r.resets, r.lines, r.offsets)
	}

	// An offset that is not at a line boundary means the cache is wrong.
	r = recorder{}
	if _, err := NewTailer(p, 5).Read(&r); err != nil {
		t.Fatal(err)
	}
	if r.resets != 1 || len(r.lines) != 2 {
		t.Errorf("misaligned offset: resets=%d lines=%q", r.resets, r.lines)
	}
}

func TestTailerLongLine(t *testing.T) {
	big := strings.Repeat("x", 500*1024)
	text := line(`"type":"assistant"`, `"timestamp":"2026-01-10T10:00:00.000Z"`,
		`"message":{"id":"m1","model":"claude-test-1","content":[{"type":"text","text":"`+big+`"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	p := writeFile(t, "t.jsonl", text+"\n")

	acc := NewAccumulator()
	var conv Conversation
	if _, err := NewTailer(p, 0).Read(acc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTailer(p, 0).Read(&conv); err != nil {
		t.Fatal(err)
	}
	st := acc.State()
	if st.Lines != 1 || st.Skipped != 0 || st.Messages != 1 || st.Usage["claude-test-1"].Output != 2 {
		t.Errorf("state after long line: %+v", st)
	}
	if es := conv.Entries(0); len(es) != 1 || len(es[0].Text) != len(big) {
		t.Errorf("long text entry not kept verbatim")
	}
}

func TestTailerMissingFile(t *testing.T) {
	_, err := NewTailer(filepath.Join(t.TempDir(), "none.jsonl"), 0).Read(&recorder{})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

// A file replaced by different content of the same size, or by longer
// content whose byte before the old offset happens to be a newline, ends
// every line where the old one did. Only the content can tell them apart.
func TestTailerNoticesReplacementWithTheSameLineBoundaries(t *testing.T) {
	for _, tt := range []struct{ name, old, new string }{
		{"same size", "{\"p\":\"AAAA\"}\n{\"u\":100}\n", "{\"p\":\"BBBB\"}\n{\"u\":300}\n"},
		{"longer", "{\"p\":\"AAAA\"}\n{\"u\":100}\n", "{\"p\":\"BBBB\"}\n{\"u\":300}\n{\"more\":1}\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := writeFile(t, "t.jsonl", tt.old)
			tl := NewTailer(p, 0)
			var r recorder
			if _, err := tl.Read(&r); err != nil {
				t.Fatal(err)
			}
			fp := tl.Fingerprint()
			if err := os.WriteFile(p, []byte(tt.new), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := tl.Read(&r); err != nil {
				t.Fatal(err)
			}
			if r.resets != 1 || strings.Join(r.lines, "") != tt.new {
				t.Errorf("resets=%d lines=%q", r.resets, r.lines)
			}

			// A tailer resumed from a remembered offset and fingerprint
			// checks the same way.
			if err := os.WriteFile(p, []byte(tt.old), 0o600); err != nil {
				t.Fatal(err)
			}
			r = recorder{}
			resumed := ResumeTailer(p, int64(len(tt.old)), fp)
			if _, err := resumed.Read(&r); err != nil || r.resets != 0 || len(r.lines) != 0 {
				t.Errorf("unchanged file: resets=%d lines=%q err=%v", r.resets, r.lines, err)
			}
			if err := os.WriteFile(p, []byte(tt.new), 0o600); err != nil {
				t.Fatal(err)
			}
			r = recorder{}
			resumed = ResumeTailer(p, int64(len(tt.old)), fp)
			if _, err := resumed.Read(&r); err != nil || r.resets != 1 || strings.Join(r.lines, "") != tt.new {
				t.Errorf("replaced file: resets=%d lines=%q err=%v", r.resets, r.lines, err)
			}
		})
	}
}
