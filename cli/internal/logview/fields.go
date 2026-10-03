package logview

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	messageKeys  = []string{"msg", "message", "event"}
	messagePaths = [][]string{{"msg"}, {"message"}, {"fields", "message"}, {"event"}}
	errorPaths   = [][]string{{"err", "stack"}, {"error", "stack"}, {"stack"}, {"exception"}, {"err"}, {"error"}}
	levelKeys    = []string{"level", "severity", "levelname", "lvl"}
	timeKeys     = []string{"time", "ts", "timestamp"}
	epochNumber  = regexp.MustCompile(`^(\d+)(?:\.(\d+))?$`)
)

const (
	earliestEpochSeconds = 1e8
	earliestEpochMillis  = 1e11
	latestEpochMillis    = 1e14
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
		if value, ok := lookup(fields, path).(string); ok && value != "" {
			remove(fields, path)
			return value, true
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
		if parsed, ok := timeOf(fields[key]); ok {
			delete(fields, key)
			return parsed
		}
	}
	return time.Time{}
}

func timeOf(value any) (time.Time, bool) {
	switch value := value.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed, true
		}
		return epoch(value)
	case json.Number:
		return epoch(value.String())
	}
	return time.Time{}, false
}

func epoch(number string) (time.Time, bool) {
	match := epochNumber.FindStringSubmatch(number)
	if match == nil {
		return time.Time{}, false
	}
	whole, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	switch {
	case whole >= latestEpochMillis:
		return time.Time{}, false
	case whole >= earliestEpochMillis:
		return time.UnixMilli(whole).Add(time.Duration(scaleFraction(match[2], 6))), true
	case whole >= earliestEpochSeconds:
		return time.Unix(whole, scaleFraction(match[2], 9)), true
	}
	return time.Time{}, false
}

func scaleFraction(digits string, places int) int64 {
	if len(digits) > places {
		digits = digits[:places]
	}
	scaled, _ := strconv.ParseInt(digits+strings.Repeat("0", places-len(digits)), 10, 64)
	return scaled
}

func lookup(fields map[string]any, path []string) any {
	var current any = fields
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
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
