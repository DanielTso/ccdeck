package claude

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
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
// A Tailer notices a file that was replaced rather than appended to: one
// that shrank, one whose offset no longer ends a line, and, by a
// Fingerprint of the content before the offset, one rewritten with the same
// line boundaries.
//
// A Tailer is not safe for concurrent use.
type Tailer struct {
	path   string
	offset int64
	fp     uint64 // Fingerprint at offset; 0 when not known yet
}

// NewTailer returns a Tailer for path that starts reading at offset, which
// must be 0 or the end of a complete line (for example a cached
// State.Offset).
func NewTailer(path string, offset int64) *Tailer {
	return &Tailer{path: path, offset: max(offset, 0)}
}

// ResumeTailer is NewTailer for an offset whose Fingerprint was remembered:
// the first Read starts over unless the content before offset still has
// that fingerprint. fp 0 means unknown, as with NewTailer.
func ResumeTailer(path string, offset int64, fp uint64) *Tailer {
	t := NewTailer(path, offset)
	if t.offset > 0 {
		t.fp = fp
	}
	return t
}

// Fingerprint identifies the content before the offset, so that a later
// Read, or a ResumeTailer, can tell a file that only grew from one that was
// replaced. It is 0 before the first Read and at offset 0.
func (t *Tailer) Fingerprint() uint64 { return t.fp }

// fpWindow is how many bytes at each end of the content before an offset
// go into its fingerprint.
const fpWindow = 4096

// Fingerprint hashes the content of r before offset: its first and last
// few KB, and the offset. Appending to r leaves it unchanged; a rewrite
// that touches either end changes it. It is never 0 for offset > 0.
func Fingerprint(r io.ReaderAt, offset int64) (uint64, error) {
	if offset <= 0 {
		return 0, nil
	}
	h := fnv.New64a()
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(offset))
	_, _ = h.Write(n[:]) // a hash.Hash never fails to write
	buf := make([]byte, min(offset, fpWindow))
	for _, at := range []int64{0, offset - int64(len(buf))} {
		if _, err := r.ReadAt(buf, at); err != nil {
			return 0, err
		}
		_, _ = h.Write(buf)
	}
	return max(h.Sum64(), 1), nil
}

// Path is the file being read.
func (t *Tailer) Path() string { return t.path }

// Offset is the byte offset just past the last complete line delivered.
func (t *Tailer) Offset() int64 { return t.offset }

// Read delivers every complete line from the current offset to s and
// returns how many lines it delivered. If the file is smaller than the
// offset, the byte before the offset is not a newline, or the content before
// the offset no longer matches its fingerprint (the file was replaced), Read
// calls s.Reset and starts from the beginning. A missing file is an error
// that satisfies errors.Is(err, fs.ErrNotExist).
func (t *Tailer) Read(s Sink) (int, error) { return t.ReadContext(context.Background(), s) }

// ReadContext is Read that stops between lines once ctx is done, returning
// ctx's error with the lines delivered so far. The Tailer stays at a line
// boundary, so the sink's state is consistent and a later Read continues.
func (t *Tailer) ReadContext(ctx context.Context, s Sink) (int, error) {
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
		if err := ctx.Err(); err != nil {
			return n, errors.Join(err, t.remember(f, n > 0))
		}
		line, err := r.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				// A partial line, if any, waits for its newline.
				return n, t.remember(f, n > 0)
			}
			return n, fmt.Errorf("read %s: %w", t.path, err)
		}
		s.Line(t.offset, line)
		t.offset += int64(len(line))
		n++
	}
}

// remember takes the fingerprint at the offset after a Read, when the
// offset moved or the fingerprint is not known yet.
func (t *Tailer) remember(f *os.File, moved bool) error {
	if !moved && (t.fp != 0 || t.offset == 0) {
		return nil
	}
	fp, err := Fingerprint(f, t.offset)
	if err != nil {
		return fmt.Errorf("read %s: %w", t.path, err)
	}
	t.fp = fp
	return nil
}

// verify resets the tailer and the sink when the file no longer extends the
// content already delivered. It costs one stat and a few small reads, so it
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
		return nil
	}
	if t.fp == 0 {
		return nil // nothing to compare with; the newline check has to do
	}
	fp, err := Fingerprint(f, t.offset)
	if err != nil {
		return fmt.Errorf("read %s: %w", t.path, err)
	}
	if fp != t.fp {
		t.reset(s)
	}
	return nil
}

// Restart starts over from the beginning of the file, calling s.Reset, as
// when the file is known to have been rewritten.
func (t *Tailer) Restart(s Sink) { t.reset(s) }

func (t *Tailer) reset(s Sink) {
	t.offset, t.fp = 0, 0
	s.Reset()
}
