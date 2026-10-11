package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type CacheRule struct {
	Path       string `json:"path"`
	Revalidate int    `json:"revalidate"`
	Expire     int    `json:"expire,omitempty"`
}

func RefuseCacheRules(rules []CacheRule) error {
	for i, rule := range rules {
		if err := rule.refuse(); err != nil {
			return fmt.Errorf("cache rule %d (%q): %w", i, rule.Path, err)
		}
	}
	return nil
}

func (r CacheRule) refuse() error {
	switch {
	case !strings.HasPrefix(r.Path, "/"):
		return errors.New("a path starts with a slash")
	case strings.ContainsAny(r.Path, "?#"):
		return errors.New("a path names no query or fragment")
	case strings.Contains(strings.TrimSuffix(r.Path, "*"), "*"),
		strings.HasSuffix(r.Path, "*") && !strings.HasSuffix(r.Path, "/*"):
		return errors.New("a path takes one *, and only as its whole last segment, as in /blog/*")
	case r.Revalidate < 1:
		return errors.New("revalidate is a whole number of seconds, at least 1")
	case r.Expire != 0 && r.Expire <= r.Revalidate:
		return errors.New("expire is a whole number of seconds longer than revalidate")
	}
	return nil
}

func EncodeCacheRules(rules []CacheRule) string {
	if len(rules) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(rules)
	return string(encoded)
}
