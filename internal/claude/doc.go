// Package claude reads the files Claude Code keeps under its configuration
// directory (normally ~/.claude): session transcripts, subagent transcripts,
// the live session registry, prompt history, the model catalog and the price
// cache.
//
// Everything here is a pure function or a small single-goroutine type: no
// watching, no background work. The formats are undocumented, so decoding is
// tolerant: unknown line types and fields are ignored, malformed lines are
// counted and skipped, and nothing panics on surprising input. Lines that
// carry prompt plumbing or account details (attachment, api-request*,
// session-owner) are ignored: nothing from them, not even a timestamp,
// reaches a summary or an entry.
//
// # Reading a transcript
//
// A [Tailer] reads the complete lines appended to one transcript since its
// last call and hands them to a [Sink]. Two sinks are provided: an
// [Accumulator] folds lines into the numbers of a session summary, and a
// [Conversation] turns them into displayable entries.
//
//	acc := claude.NewAccumulator()
//	t := claude.NewTailer(path, 0)
//	if _, err := t.Read(acc); err != nil { ... }
//	s := acc.Session(claude.SessionInput{ID: id, Path: path, ...})
//
// # Caching and resuming
//
// [Accumulator.State] returns a value that encodes with encoding/json and
// holds derived numbers and identifiers only, never conversation text. To
// resume, call [RestoreAccumulator] with the cached State and the transcript,
// then continue with NewTailer(path, state.Offset). The State is enough to
// keep every count exact across a resume:
//
//   - Usage is counted once per assistant message id. The lines of one message
//     are adjacent, so remembering only the last message id and the usage
//     already counted for it (State.Last) deduplicates correctly, with memory
//     that does not grow with the file.
//   - Titles and prompts are text, so the State records the byte offsets of
//     the lines they came from, and RestoreAccumulator re-reads just those
//     lines. If a recorded line no longer says what it said, the transcript
//     was rewritten and RestoreAccumulator returns [ErrStaleState]: parse the
//     file from the start instead.
//
// The caller decides whether a cached State may be used at all (same path,
// file not smaller than State.Offset). On every read the Tailer also checks
// that the byte before its offset is a newline, and resets its sink and
// starts over when the file shrank or was replaced.
package claude
