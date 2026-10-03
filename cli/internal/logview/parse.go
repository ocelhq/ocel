package logview

import (
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	timestampPattern = `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`
	levelPattern     = `TRACE|DEBUG|INFO|WARNING|WARN|ERROR|FATAL|CRITICAL`
)

var levelPrefix = regexp.MustCompile(`(?s)^(?:(?P<lead>` + timestampPattern + `)\s+)?` +
	`(?:\[(?:(?P<inner>` + timestampPattern + `)\s+)?(?P<bracketed>` + levelPattern + `)(?:\s+(?P<target>[^\]\s]+))?\]` +
	`|(?P<bare>` + levelPattern + `)(?::(?P<logger>[A-Za-z_][\w.]*):|:?(?:\s+|$)))` +
	`\s*(?P<rest>.*)$`)

func Parse(raw string) Entry {
	if fields, ok := parseJSON(raw); ok {
		return entryFrom(raw, fields)
	}
	if fields, ok := parseLogfmt(raw); ok {
		return entryFrom(raw, fields)
	}
	if entry, ok := parseLevelPrefix(raw); ok {
		return entry
	}
	return Entry{Message: raw, Raw: raw}
}

func parseJSON(raw string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func parseLogfmt(raw string) (map[string]any, bool) {
	fields := map[string]any{}
	pairs := 0
	rest := strings.TrimSpace(raw)
	for rest != "" {
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 || strings.ContainsAny(rest[:eq], " \t\"") {
			return nil, false
		}
		key := rest[:eq]
		rest = rest[eq+1:]
		var value string
		if strings.HasPrefix(rest, `"`) {
			end := closingQuote(rest)
			if end < 0 {
				return nil, false
			}
			unquoted, err := strconv.Unquote(rest[:end])
			if err != nil {
				return nil, false
			}
			value, rest = unquoted, rest[end:]
		} else {
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				end = len(rest)
			}
			value, rest = rest[:end], rest[end:]
		}
		fields[key] = value
		pairs++
		rest = strings.TrimLeft(rest, " \t")
	}
	_, hasLevel := fields["level"]
	_, hasMessage := fields["msg"]
	if pairs < 2 || (!hasLevel && !hasMessage) {
		return nil, false
	}
	return fields, true
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

func parseLevelPrefix(raw string) (Entry, bool) {
	match := levelPrefix.FindStringSubmatch(raw)
	if match == nil {
		return Entry{}, false
	}
	group := func(name string) string { return match[levelPrefix.SubexpIndex(name)] }

	entry := Entry{
		Raw:     raw,
		Message: group("rest"),
		Level:   levelOf(group("bracketed") + group("bare")),
	}
	if stamp := group("lead") + group("inner"); stamp != "" {
		entry.Time, _ = time.Parse(time.RFC3339, stamp)
	}
	for _, name := range []string{"target", "logger"} {
		if value := group(name); value != "" {
			if entry.Fields == nil {
				entry.Fields = map[string]any{}
			}
			entry.Fields[name] = value
		}
	}
	return entry, true
}
