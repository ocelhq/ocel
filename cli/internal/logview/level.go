package logview

import (
	"cmp"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
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

var levelNames = [...]string{LevelUnknown: "", LevelDebug: "debug", LevelInfo: "info", LevelWarn: "warn", LevelError: "error"}

func ParseLevel(name string) (Level, bool) {
	index := slices.Index(levelNames[:], strings.ToLower(name))
	if index <= int(LevelUnknown) {
		return LevelUnknown, false
	}
	return Level(index), true
}

func (l Level) String() string {
	if l < LevelUnknown || int(l) >= len(levelNames) {
		return ""
	}
	return levelNames[l]
}

func ResolveLevel(parsed Level, severity string, failure bool) Level {
	switch {
	case failure:
		return LevelError
	case parsed != LevelUnknown:
		return parsed
	}
	return levelOf(severity)
}

var levelOffset = regexp.MustCompile(`[+-]\d+$`)

var levelSpellings = map[string]Level{
	"TRACE":     LevelDebug,
	"DEBUG":     LevelDebug,
	"INFO":      LevelInfo,
	"NOTICE":    LevelInfo,
	"WARN":      LevelWarn,
	"WARNING":   LevelWarn,
	"ERROR":     LevelError,
	"FATAL":     LevelError,
	"CRITICAL":  LevelError,
	"PANIC":     LevelError,
	"DPANIC":    LevelError,
	"ALERT":     LevelError,
	"EMERGENCY": LevelError,
}

func buildLevelPattern() string {
	names := slices.SortedFunc(maps.Keys(levelSpellings), func(left, right string) int {
		return cmp.Or(cmp.Compare(len(right), len(left)), strings.Compare(left, right))
	})
	return strings.Join(names, "|")
}

func levelOf(value any) Level {
	switch value := value.(type) {
	case string:
		name := strings.ToUpper(strings.TrimSpace(value))
		return levelSpellings[levelOffset.ReplaceAllString(name, "")]
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return LevelUnknown
		}
		switch {
		case number <= 0:
			return LevelUnknown
		case number <= 20:
			return LevelDebug
		case number <= 30:
			return LevelInfo
		case number <= 40:
			return LevelWarn
		default:
			return LevelError
		}
	}
	return LevelUnknown
}
