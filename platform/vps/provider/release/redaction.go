package release

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	LogsWithheld     = "withheld: the values to redact from them could not be read"
	redactedMinBytes = 8
)

func EnvCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote("{{json .Config.Env}}") + " " + shell.Quote(name)
}

func (c *Containers) readRedactedLogs(ctx context.Context, elevation ssh.Acting, name, command string) string {
	logs, err := c.box.Said(ctx, elevation, command)
	if err != nil {
		return "(could not be read: " + err.Error() + ")"
	}
	if logs == "" {
		return ""
	}
	read, err := c.box.Stream(ctx, elevation, EnvCommand(name), nil)
	var env []string
	switch {
	case err != nil:
		return "(" + LogsWithheld + ": " + err.Error() + ")"
	case read.Code != 0:
		return "(" + LogsWithheld + ": " + ssh.Reason(read) + ")"
	case json.Unmarshal([]byte(read.Stdout), &env) != nil:
		return "(" + LogsWithheld + ": the container's environment did not read as a JSON list)"
	}
	return redact(logs, env)
}

type redactedSpan struct {
	start, end int
	keys       []string
}

func redact(logs string, env []string) string {
	var spans []redactedSpan
	for _, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		if len(value) < redactedMinBytes {
			continue
		}
		for at := 0; ; {
			found := strings.Index(logs[at:], value)
			if found < 0 {
				break
			}
			start := at + found
			spans = append(spans, redactedSpan{start: start, end: start + len(value), keys: []string{key}})
			at = start + 1
		}
	}
	slices.SortStableFunc(spans, func(a, b redactedSpan) int { return cmp.Compare(a.start, b.start) })
	var merged []redactedSpan
	for _, span := range spans {
		if last := len(merged) - 1; last >= 0 && span.start <= merged[last].end {
			merged[last].end = max(merged[last].end, span.end)
			if !slices.Contains(merged[last].keys, span.keys[0]) {
				merged[last].keys = append(merged[last].keys, span.keys[0])
			}
			continue
		}
		merged = append(merged, span)
	}
	var redacted strings.Builder
	written := 0
	for _, span := range merged {
		redacted.WriteString(logs[written:span.start] + "[redacted " + strings.Join(span.keys, ", ") + "]")
		written = span.end
	}
	redacted.WriteString(logs[written:])
	return redacted.String()
}
