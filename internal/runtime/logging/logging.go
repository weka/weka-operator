// Package logging builds the pod runtime's logger.
package logging

import (
	"bytes"
	"io"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	obslogger "github.com/weka/go-weka-observability/logger"
)

const (
	idsPart    = "ids"
	blocksPart = "blocks"
)

type block struct{ key, text string }

// New mirrors obslogger.NewZerologrWithLoggerNameInsteadCaller, but in raw/plain format
// prints captured output and other multi-line fields as indented blocks and shows trace/span
// IDs as a short prefix.
func New() logr.Logger {
	cfg := obslogger.NewConfigFromEnv(obslogger.DefaultConfig()).Format
	zl := zerolog.New(writer(obslogger.GetStderrWriterFromFormat(cfg))).
		Level(cfg.Level).With().Timestamp().Logger()
	zerologr.NameFieldName = "caller"
	return zerologr.New(&zl)
}

func writer(w io.Writer) io.Writer {
	cw, ok := w.(zerolog.ConsoleWriter)
	if !ok {
		return w
	}
	cw.FieldsExclude = []string{"trace_id", "span_id", idsPart, blocksPart}
	cw.PartsOrder = []string{zerolog.TimestampFieldName, zerolog.LevelFieldName, idsPart, zerolog.CallerFieldName, zerolog.MessageFieldName}
	cw.FormatPrepare = func(evt map[string]any) error {
		t, tok := evt["trace_id"].(string)
		s, sok := evt["span_id"].(string)
		if tok && sok && len(s) >= 8 {
			evt[idsPart] = "[" + t + "/" + s[:8] + "]"
		}
		evt[blocksPart] = blocks(evt)
		return nil
	}
	cw.FormatPartValueByName = func(v any, name string) string {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	cw.FormatExtra = writeBlocks
	return cw
}

// blocks moves output, and any other multi-line string field, out of the inline key=value list.
func blocks(evt map[string]any) []block {
	var out []block
	for k, v := range evt {
		if text, ok := v.(string); ok && k != "output" && k != zerolog.MessageFieldName && strings.Contains(text, "\n") {
			out = append(out, block{k, text})
			delete(evt, k)
		}
	}
	slices.SortFunc(out, func(a, b block) int { return strings.Compare(a.key, b.key) })
	if text, ok := evt["output"].(string); ok {
		out = append(out, block{"output", text})
		delete(evt, "output")
	}
	return out
}

// writeBlocks prints output unlabeled under the line and other blocks as "key:" plus deeper-indented
// lines, with NULs and surrounding blank lines stripped.
func writeBlocks(evt map[string]any, buf *bytes.Buffer) error {
	bs, ok := evt[blocksPart].([]block)
	if !ok {
		return nil
	}
	for _, b := range bs {
		indent := "\n  "
		if b.key != "output" {
			buf.WriteString("\n  " + b.key + ":")
			indent = "\n    "
		}
		text := strings.TrimLeft(strings.ReplaceAll(b.text, "\x00", ""), "\r\n")
		text = strings.TrimRight(text, " \t\r\n")
		for text != "" {
			var line string
			line, text, _ = strings.Cut(text, "\n")
			buf.WriteString(indent)
			buf.WriteString(line)
		}
	}
	return nil
}
