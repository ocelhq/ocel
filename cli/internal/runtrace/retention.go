package runtrace

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const retainedRuns = 10

var releasedRunFileSuffixes = []string{".ndjson", ".otlp.json", ".otlp.json.partial"}

func prune(runs string, keep int, cutoff time.Time) error {
	entries, err := os.ReadDir(runs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var dirs []runDirectory
	var errs []error
	for _, e := range entries {
		path := filepath.Join(runs, e.Name())
		if !e.IsDir() {
			if isReleasedRunFile(e.Name()) && modifiedBefore(path, cutoff) {
				errs = append(errs, os.Remove(path))
			}
			continue
		}
		dirs = append(dirs, runDirectory{path: path, modified: lastModified(path)})
	}

	if len(dirs) <= keep {
		return errors.Join(errs...)
	}
	slices.SortFunc(dirs, func(a, b runDirectory) int { return b.modified.Compare(a.modified) })
	for _, dir := range dirs[keep:] {
		if dir.modified.Before(cutoff) {
			errs = append(errs, os.RemoveAll(dir.path))
		}
	}
	return errors.Join(errs...)
}

func isReleasedRunFile(name string) bool {
	return slices.ContainsFunc(releasedRunFileSuffixes, func(suffix string) bool { return strings.HasSuffix(name, suffix) })
}

func modifiedBefore(path string, cutoff time.Time) bool {
	info, err := os.Lstat(path)
	return err == nil && info.ModTime().Before(cutoff)
}

func lastModified(dir string) time.Time {
	var newest time.Time
	if info, err := os.Lstat(dir); err == nil {
		newest = info.ModTime()
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}
