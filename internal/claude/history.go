package claude

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/DanielTso/ccdeck/internal/core"
)

// ReadHistory reads history.jsonl (Paths.HistoryFile): one hit per prompt
// the user submitted, in file order (oldest first). Malformed lines and
// lines without text are skipped. A missing file is an error satisfying
// errors.Is(err, fs.ErrNotExist).
func ReadHistory(path string) ([]core.PromptHit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush

	var hits []core.PromptHit
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 && (err == nil || errors.Is(err, io.EOF)) {
			var h struct {
				Display   string `json:"display"`
				Timestamp int64  `json:"timestamp"`
				SessionID string `json:"sessionId"`
			}
			if decodeTolerant(line, &h) == nil && h.Display != "" {
				hit := core.PromptHit{SessionID: h.SessionID, Text: h.Display}
				if h.Timestamp > 0 {
					hit.Time = millis(h.Timestamp)
				}
				hits = append(hits, hit)
			}
		}
		if errors.Is(err, io.EOF) {
			return hits, nil
		}
		if err != nil {
			return hits, fmt.Errorf("read %s: %w", path, err)
		}
	}
}
