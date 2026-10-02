package taskruns

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultRunPage = 100
	MaxRunPage     = 1000
)

var ErrUnknownCursor = errors.New("the cursor is not one a listing returned")

func PageLimit(requested int) int {
	if requested <= 0 {
		return DefaultRunPage
	}
	return min(requested, MaxRunPage)
}

func CursorOf(at time.Time, execution string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "/" + execution))
}

func ParseCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err == nil {
		at, execution, cut := strings.Cut(string(raw), "/")
		if parsed, parseErr := time.Parse(time.RFC3339Nano, at); cut && parseErr == nil {
			return parsed, execution, nil
		}
	}
	return time.Time{}, "", fmt.Errorf("cursor %q: %w", cursor, ErrUnknownCursor)
}
