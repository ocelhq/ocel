package runtrace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const RunRetention = 10

func Prune(dir string, keep int, cutoff time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	type group struct {
		stem   string
		files  []string
		newest int64
	}
	groups := map[string]*group{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		stem := name
		if i := strings.Index(name, "."); i > 0 {
			stem = name[:i]
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		g, ok := groups[stem]
		if !ok {
			g = &group{stem: stem}
			groups[stem] = g
		}
		g.files = append(g.files, filepath.Join(dir, name))
		if mtime := info.ModTime().UnixNano(); mtime > g.newest {
			g.newest = mtime
		}
	}

	if len(groups) <= keep {
		return nil
	}

	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].newest > ordered[j].newest })

	var firstErr error
	for _, g := range ordered[keep:] {
		if !time.Unix(0, g.newest).Before(cutoff) {
			continue
		}
		for _, f := range g.files {
			if err := os.Remove(f); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
