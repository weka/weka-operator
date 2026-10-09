package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func rawLine(t *testing.T, fields map[string]any) string {
	t.Helper()
	var buf bytes.Buffer
	zl := zerolog.New(writer(zerolog.ConsoleWriter{Out: &buf, NoColor: true}))
	zl.Info().Fields(fields).Msg("stdout")
	return buf.String()
}

func TestRawOutputIsIndentedBlock(t *testing.T) {
	got := rawLine(t, map[string]any{"output": "a b\nc d\n\n\x00\x00\n"})
	want := "INF stdout\n  a b\n  c d\n"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("got %q, want suffix %q", got, want)
	}
}

func TestRawOutputKeepsInteriorBlankLinesAndStripsNULs(t *testing.T) {
	got := rawLine(t, map[string]any{"output": "a\x00\n\nb"})
	if !strings.HasSuffix(got, "stdout\n  a\n  \n  b\n") {
		t.Fatalf("got %q", got)
	}
}

func TestRawOutputIsNotTruncated(t *testing.T) {
	big := strings.Repeat("line\n", 5000)
	if got := strings.Count(rawLine(t, map[string]any{"output": big}), "\n  line"); got != 5000 {
		t.Fatalf("lines = %d, want 5000", got)
	}
}

func TestRawEmptyOutputAddsNothing(t *testing.T) {
	if got := rawLine(t, map[string]any{"output": "\x00\n"}); got != "<nil> INF stdout\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRawShortIDsPrefix(t *testing.T) {
	got := rawLine(t, map[string]any{
		"trace_id": "ab12cd34ffffffffffffffffffffffff",
		"span_id":  "3f9a1c02eeeeeeee",
		"code":     0,
	})
	if !strings.Contains(got, "INF [ab12cd34ffffffffffffffffffffffff/3f9a1c02] stdout code=0") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "eeee") || strings.Contains(got, "ids=") {
		t.Fatalf("full span ID or ids field leaked: %q", got)
	}
}

func TestJSONIsUnchanged(t *testing.T) {
	var buf bytes.Buffer
	if w := writer(&buf); w != &buf {
		t.Fatalf("json writer was wrapped")
	}
	zl := zerolog.New(writer(&buf))
	zl.Info().Str("output", "a\nb\x00").Str("span_id", "s").Str("trace_id", "t").Msg("stdout")
	var evt map[string]any
	if err := json.Unmarshal(buf.Bytes(), &evt); err != nil {
		t.Fatalf("json: %v", err)
	}
	if evt["output"] != "a\nb\x00" || evt["span_id"] != "s" || evt["trace_id"] != "t" {
		t.Fatalf("evt = %v", evt)
	}
}

func TestRawMultiLineFieldsAreLabeledBlocks(t *testing.T) {
	got := rawLine(t, map[string]any{
		"command": "sh -c \nCONFFILE=x\nsed -i '$d' \"$CONFFILE\"\n",
		"err":     "exit status 1\nmore",
		"code":    1,
	})
	want := "INF stdout code=1\n  command:\n    sh -c \n    CONFFILE=x\n    sed -i '$d' \"$CONFFILE\"\n  err:\n    exit status 1\n    more\n"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("got %q, want suffix %q", got, want)
	}
}

func TestRawSingleLineFieldsStayInline(t *testing.T) {
	got := rawLine(t, map[string]any{"command": "weka local ps", "output": "x"})
	if !strings.HasSuffix(got, "INF stdout command=\"weka local ps\"\n  x\n") {
		t.Fatalf("got %q", got)
	}
}
