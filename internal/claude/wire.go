package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// wireLine is the subset of a transcript line this package reads. Message is
// decoded separately, and only for user and assistant lines.
type wireLine struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	Timestamp        string          `json:"timestamp"`
	CWD              string          `json:"cwd"`
	GitBranch        string          `json:"gitBranch"`
	Version          string          `json:"version"`
	IsSidechain      bool            `json:"isSidechain"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	Origin           *wireOrigin     `json:"origin"`
	Message          json.RawMessage `json:"message"`

	AITitle      string  `json:"aiTitle"`
	CustomTitle  string  `json:"customTitle"`
	LastPrompt   string  `json:"lastPrompt"`
	TotalCostUSD float64 `json:"totalCostUSD"`

	CompactMetadata *struct {
		PreTokens int64 `json:"preTokens"`
	} `json:"compactMetadata"`
}

type wireOrigin struct {
	Kind string `json:"kind"`
}

type wireMessage struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *wireUsage      `json:"usage"`
}

type wireBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type wireUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails *struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	ServerToolUse *struct {
		WebSearchRequests int64 `json:"web_search_requests"`
	} `json:"server_tool_use"`
	// Speed is the tier the request was served in: "standard" or "fast".
	Speed string `json:"speed"`
	// Iterations lists the requests of a message that took several; decoded
	// only for models with a long-prompt tier.
	Iterations json.RawMessage `json:"iterations"`
}

// usage converts the wire form. Without the cache_creation breakdown every
// cache write counts as a 5-minute write; with it, the 1-hour share is taken
// from the breakdown and the rest of the total is 5-minute.
func (w *wireUsage) usage() core.Usage {
	if w == nil {
		return core.Usage{}
	}
	u := core.Usage{
		Input:     w.InputTokens,
		Output:    w.OutputTokens,
		CacheRead: w.CacheReadInputTokens,
	}
	write := w.CacheCreationInputTokens
	if cc := w.CacheCreation; cc != nil {
		if write == 0 {
			write = cc.Ephemeral5m + cc.Ephemeral1h
		}
		u.CacheWrite1h = min(cc.Ephemeral1h, write)
	}
	u.CacheWrite5m = write - u.CacheWrite1h
	if d := w.OutputTokensDetails; d != nil {
		u.Thinking = d.ThinkingTokens
	}
	if s := w.ServerToolUse; s != nil {
		u.WebSearches = s.WebSearchRequests
	}
	return u
}

// errNotObject rejects JSON that is valid but not an object.
var errNotObject = errors.New("not a JSON object")

// decodeTolerant unmarshals a JSON object into the struct v. A field whose
// JSON type does not match is left at its zero value instead of failing the
// whole value: encoding/json keeps decoding after a type mismatch and
// reports it at the end, so the rest of v is populated.
func decodeTolerant(data []byte, v any) error {
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return errNotObject
	}
	err := json.Unmarshal(data, v)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return nil
	}
	return err
}

// decodeLine decodes one transcript line. ok is false for a malformed line.
func decodeLine(line []byte) (wireLine, bool) {
	var l wireLine
	if err := decodeTolerant(line, &l); err != nil {
		return wireLine{}, false
	}
	return l, true
}

// message decodes l.Message; a missing or malformed message is empty.
func (l *wireLine) message() wireMessage {
	var m wireMessage
	if len(l.Message) > 0 {
		_ = decodeTolerant(l.Message, &m)
	}
	return m
}

// time parses the line's timestamp; zero when absent or unparsable.
func (l *wireLine) time() time.Time {
	if l.Timestamp == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		return time.Time{}
	}
	return t
}

// blocks decodes message content, which is either a string (one text block)
// or an array of blocks.
func blocks(content json.RawMessage) (text string, isString bool, bs []wireBlock) {
	if len(content) == 0 {
		return "", false, nil
	}
	if content[0] == '"' {
		if err := json.Unmarshal(content, &text); err == nil {
			return text, true, nil
		}
		return "", false, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return "", false, nil
	}
	bs = make([]wireBlock, 0, len(raw))
	for _, r := range raw {
		var b wireBlock
		if decodeTolerant(r, &b) == nil {
			bs = append(bs, b)
		}
	}
	return "", false, bs
}
