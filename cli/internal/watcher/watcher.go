// Package watcher watches a set of paths for filesystem changes and invokes a
// callback once per debounced burst of activity.
package watcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Set is what a watch covers: every path under each of Dirs at any depth,
// including subdirectories created after the watch started, plus each of Files
// and nothing else beside it. A file's directory has to be watched to see the
// file change, but only that one path in it counts and no subdirectory of it is
// ever watched — a project root holds node_modules, .next and dist.
type Set struct {
	Dirs  []string
	Files []string
}

func (s Set) covers(path string) bool {
	for _, file := range s.Files {
		if path == file {
			return true
		}
	}
	return s.coversTree(path)
}

// coversTree reports whether path sits under one of Dirs, at any depth — a
// subdirectory created after the watch started belongs to the same tree as the
// files already there.
func (s Set) coversTree(path string) bool {
	for _, dir := range s.Dirs {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Watch establishes a watch over set and, until ctx is done, invokes onChange
// once after every quiet period of debounce following one or more changes to a
// path set covers. Errors the underlying watcher reports while running (e.g.
// the inotify limit is hit) are passed to onError, which may be nil to ignore
// them. Watch returns as soon as the watch is established (or fails); the event
// loop runs in the background.
func Watch(ctx context.Context, set Set, debounce time.Duration, onChange func(), onError func(error)) error {
	_, err := start(ctx, set, debounce, onChange, onError)
	return err
}

func start(ctx context.Context, set Set, debounce time.Duration, onChange func(), onError func(error)) (*fsnotify.Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for _, dir := range set.Dirs {
		if err := fsw.Add(dir); err != nil {
			fsw.Close()
			return nil, err
		}
	}
	for _, file := range set.Files {
		if err := fsw.Add(filepath.Dir(file)); err != nil {
			fsw.Close()
			return nil, err
		}
	}

	go run(ctx, fsw, set, debounce, onChange, onError)
	return fsw, nil
}

func run(ctx context.Context, fsw *fsnotify.Watcher, set Set, debounce time.Duration, onChange func(), onError func(error)) {
	defer fsw.Close()

	timer := time.NewTimer(debounce)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-fsw.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) && set.coversTree(event.Name) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = fsw.Add(event.Name)
				}
			}
			if !set.covers(event.Name) {
				continue
			}
			if armed && !timer.Stop() {
				<-timer.C
			}
			timer.Reset(debounce)
			armed = true
		case <-timer.C:
			armed = false
			onChange()
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			if onError != nil {
				onError(err)
			}
		}
	}
}
