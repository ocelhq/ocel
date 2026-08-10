package watcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

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

func (s Set) coversTree(path string) bool {
	for _, dir := range s.Dirs {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

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
