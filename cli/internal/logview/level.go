package logview

import (
	"encoding/json"
	"regexp"
	"strings"
)

type Level int

const (
	LevelUnknown Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
)

var levelOffset = regexp.MustCompile(`[+-]\d+$`)

var levelNames = map[string]Level{
	"TRACE":    LevelDebug,
	"DEBUG":    LevelDebug,
	"INFO":     LevelInfo,
	"WARN":     LevelWarn,
	"WARNING":  LevelWarn,
	"ERROR":    LevelError,
	"FATAL":    LevelError,
	"CRITICAL": LevelError,
	"PANIC":    LevelError,
	"DPANIC":   LevelError,
}

func levelOf(value any) Level {
	switch v := value.(type) {
	case string:
		name := strings.ToUpper(strings.TrimSpace(v))
		return levelNames[levelOffset.ReplaceAllString(name, "")]
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return LevelUnknown
		}
		switch {
		case n <= 0:
			return LevelUnknown
		case n <= 20:
			return LevelDebug
		case n <= 30:
			return LevelInfo
		case n <= 40:
			return LevelWarn
		default:
			return LevelError
		}
	}
	return LevelUnknown
}
