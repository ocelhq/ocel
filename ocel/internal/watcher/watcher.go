// Package watcher watches a set of directories for filesystem changes and
// invokes a callback once per debounced burst of activity.
package watcher

import (
	"context"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch adds a filesystem watch on each of dirs and, until ctx is done,
// invokes onChange once after every quiet period of debounce following one
// or more change events. A directory created under an already-watched
// directory is itself watched, so files later added under it are picked up
// too. Errors the underlying watcher reports while running (e.g. the
// inotify limit is hit) are passed to onError, which may be nil to ignore
// them. Watch returns as soon as the watch is established (or fails); the
// event loop runs in the background.
func Watch(ctx context.Context, dirs []string, debounce time.Duration, onChange func(), onError func(error)) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	for _, d := range dirs {
		if err := fsw.Add(d); err != nil {
			fsw.Close()
			return err
		}
	}

	go run(ctx, fsw, debounce, onChange, onError)
	return nil
}

func run(ctx context.Context, fsw *fsnotify.Watcher, debounce time.Duration, onChange func(), onError func(error)) {
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
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = fsw.Add(event.Name)
				}
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
