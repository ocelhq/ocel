package filewatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Paths struct {
	Dirs  []string
	Files []string
}

func (p Paths) covers(path string) bool {
	for _, file := range p.Files {
		if path == file {
			return true
		}
	}
	return p.coversTree(path)
}

func (p Paths) coversTree(path string) bool {
	for _, dir := range p.Dirs {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type Config struct {
	Paths    Paths
	Debounce time.Duration
	OnChange func()
	OnError  func(error)

	newTimer func(time.Duration) timer
}

type timer interface {
	Stop() bool
	Reset(time.Duration) bool
	C() <-chan time.Time
}

type realTimer struct{ t *time.Timer }

func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
func (r realTimer) C() <-chan time.Time        { return r.t.C }

func newRealTimer(d time.Duration) timer { return realTimer{t: time.NewTimer(d)} }

type Watcher struct {
	fsw  *fsnotify.Watcher
	done chan struct{}
}

func (w *Watcher) WatchList() []string { return w.fsw.WatchList() }

func (w *Watcher) Done() <-chan struct{} { return w.done }

func Start(ctx context.Context, cfg Config) (*Watcher, error) {
	if cfg.newTimer == nil {
		cfg.newTimer = newRealTimer
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for _, dir := range cfg.Paths.Dirs {
		if err := fsw.Add(dir); err != nil {
			fsw.Close()
			return nil, err
		}
	}
	for _, file := range cfg.Paths.Files {
		if err := fsw.Add(filepath.Dir(file)); err != nil {
			fsw.Close()
			return nil, err
		}
	}

	w := &Watcher{fsw: fsw, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		run(ctx, fsw, cfg)
	}()
	return w, nil
}

func run(ctx context.Context, fsw *fsnotify.Watcher, cfg Config) {
	defer fsw.Close()

	t := cfg.newTimer(cfg.Debounce)
	if !t.Stop() {
		<-t.C()
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
			if event.Has(fsnotify.Create) && cfg.Paths.coversTree(event.Name) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = fsw.Add(event.Name)
				}
			}
			if !cfg.Paths.covers(event.Name) {
				continue
			}
			if armed && !t.Stop() {
				<-t.C()
			}
			t.Reset(cfg.Debounce)
			armed = true
		case <-t.C():
			armed = false
			cfg.OnChange()
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			if cfg.OnError != nil {
				cfg.OnError(err)
			}
		}
	}
}
