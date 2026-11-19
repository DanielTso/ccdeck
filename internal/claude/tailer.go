package claude

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// Sink receives the lines a Tailer reads.
type Sink interface {
	// Line is called once per complete line, in file order. offset is the
	// byte offset of the line's first byte; line includes its trailing
	// newline. The slice is only valid for the duration of the call.
	Line(offset int64, line []byte)
	// Reset is called when the file shrank or was replaced: everything
	// received so far is void, and lines start again from offset 0.
	Reset()
}

// Tailer reads one JSON Lines file incrementally. Each Read delivers the
// complete lines appended since the previous Read; a partial last line (no
// trailing newline yet) is held back until it is complete. Lines may be
// arbitrarily long.
//
// A Tailer is not safe for concurrent use.
type Tailer struct {
	path   string
	offset int64
}

// NewTailer returns a Tailer for path that starts reading at offset, which
// must be 0 or the end of a complete line (for example a cached
// State.Offset).
func NewTailer(path string, offset int64) *Tailer {
	return &Tailer{path: path, offset: max(offset, 0)}
}

// Path is the file being read.
func (t *Tailer) Path() string { return t.path }

// Offset is the byte offset just past the last complete line delivered.
func (t *Tailer) Offset() int64 { return t.offset }

// Read delivers every complete line from the current offset to s and
// returns how many lines it delivered. If the file is smaller than the
// offset, or the byte before the offset is not a newline (the file was
// replaced), Read calls s.Reset and starts from the beginning. A missing
// file is an error that satisfies errors.Is(err, fs.ErrNotExist).
func (t *Tailer) Read(s Sink) (int, error) {
	f, err := os.Open(t.path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush

	if err := t.verify(f, s); err != nil {
		return 0, err
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek %s: %w", t.path, err)
	}
	r := bufio.NewReaderSize(f, 64*1024)
	n := 0
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil // a partial line, if any, waits for its newline
			}
			return n, fmt.Errorf("read %s: %w", t.path, err)
		}
		s.Line(t.offset, line)
		t.offset += int64(len(line))
		n++
	}
}

// verify resets the tailer and the sink when the file no longer extends the
// content already delivered. It costs one stat and a one-byte read, so it
// runs on every Read.
func (t *Tailer) verify(f *os.File, s Sink) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", t.path, err)
	}
	if info.Size() < t.offset {
		t.reset(s)
		return nil
	}
	if t.offset == 0 {
		return nil
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], t.offset-1); err != nil {
		return fmt.Errorf("read %s: %w", t.path, err)
	}
	if b[0] != '\n' {
		t.reset(s)
	}
	return nil
}

func (t *Tailer) reset(s Sink) {
	t.offset = 0
	s.Reset()
}
