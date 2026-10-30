package pricecache

import (
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/pricing"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

type cacheFile struct {
	Entries []entry `json:"entries"`
}

type entry struct {
	Console   string          `json:"console"`
	Key       string          `json:"key"`
	FetchedAt time.Time       `json:"fetchedAt"`
	Answer    json.RawMessage `json:"answer"`
}

func (e entry) slot() string {
	return e.Console + "\n" + e.Key
}

type cached struct {
	entry
	answer pricing.Answer
	miss   bool
}

func newCached(e entry, received *consolev1.PriceAnswer) (cached, error) {
	answer, err := readAnswer(received)
	if err != nil {
		return cached{}, err
	}
	return cached{entry: e, answer: answer, miss: received.GetMiss() != nil}, nil
}

func readCached(e entry) (cached, error) {
	var received consolev1.PriceAnswer
	if err := protojson.Unmarshal(e.Answer, &received); err != nil {
		return cached{}, err
	}
	return newCached(e, &received)
}

func (c cached) fresh(now time.Time) bool {
	ttl := pricedTTL
	if c.miss {
		ttl = missTTL
	}
	age := now.Sub(c.FetchedAt)
	return age >= 0 && age < ttl
}

func readCache(path string) map[string]cached {
	cache := map[string]cached{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	var file cacheFile
	if json.Unmarshal(raw, &file) != nil {
		return cache
	}
	for _, e := range file.Entries {
		if e.Key == "" {
			continue
		}
		if read, err := readCached(e); err == nil {
			cache[e.slot()] = read
		}
	}
	return cache
}

func writeCache(path string, cache map[string]cached) {
	kept := slices.SortedFunc(maps.Values(cache), func(a, b cached) int {
		if c := b.FetchedAt.Compare(a.FetchedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.slot(), b.slot())
	})
	if len(kept) > maxEntries {
		kept = kept[:maxEntries]
	}
	entries := make([]entry, 0, len(kept))
	for _, c := range kept {
		entries = append(entries, c.entry)
	}
	encoded, err := json.Marshal(cacheFile{Entries: entries})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	temp, err := os.CreateTemp(dir, ".answers-*.json")
	if err != nil {
		return
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(encoded)
	if closeErr := temp.Close(); writeErr != nil || closeErr != nil {
		return
	}
	_ = os.Rename(temp.Name(), path)
}
