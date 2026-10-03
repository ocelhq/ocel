package logview

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
)

var (
	messagePaths = [][]string{{"msg"}, {"message"}, {"fields", "message"}, {"event"}}
	errorPaths   = [][]string{{"err", "stack"}, {"error", "stack"}, {"stack"}, {"exception"}, {"err"}, {"error"}}
	levelKeys    = []string{"level", "severity", "levelname", "lvl"}
	timeKeys     = []string{"time", "ts", "timestamp"}
)

const (
	earliestEpochSeconds = 1e8
	earliestEpochMillis  = 1e11
)

func entryFrom(raw string, fields map[string]any) Entry {
	entry := Entry{Raw: raw, Message: raw}
	if message, ok := takeString(fields, messagePaths); ok {
		entry.Message = message
	}
	entry.Level = takeLevel(fields)
	entry.Time = takeTime(fields)
	entry.Error, _ = takeString(fields, errorPaths)
	if len(fields) > 0 {
		entry.Fields = fields
	}
	return entry
}

func takeString(fields map[string]any, paths [][]string) (string, bool) {
	for _, path := range paths {
		if s, ok := lookup(fields, path).(string); ok && s != "" {
			remove(fields, path)
			return s, true
		}
	}
	return "", false
}

func takeLevel(fields map[string]any) Level {
	for _, key := range levelKeys {
		if level := levelOf(fields[key]); level != LevelUnknown {
			delete(fields, key)
			return level
		}
	}
	return LevelUnknown
}

func takeTime(fields map[string]any) time.Time {
	for _, key := range timeKeys {
		if t, ok := timeOf(fields[key]); ok {
			delete(fields, key)
			return t
		}
	}
	return time.Time{}
}

func timeOf(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, true
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return epoch(f)
		}
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return epoch(f)
		}
	}
	return time.Time{}, false
}

func epoch(f float64) (time.Time, bool) {
	switch {
	case f >= earliestEpochMillis:
		whole := math.Floor(f)
		return time.UnixMilli(int64(whole)).Add(time.Duration((f - whole) * float64(time.Millisecond))), true
	case f >= earliestEpochSeconds:
		whole := math.Floor(f)
		return time.Unix(int64(whole), int64(math.Round((f-whole)*float64(time.Second)))), true
	}
	return time.Time{}, false
}

func lookup(fields map[string]any, path []string) any {
	var current any = fields
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}

func remove(fields map[string]any, path []string) {
	if len(path) == 1 {
		delete(fields, path[0])
		return
	}
	child, ok := fields[path[0]].(map[string]any)
	if !ok {
		return
	}
	remove(child, path[1:])
	if len(child) == 0 {
		delete(fields, path[0])
	}
}
