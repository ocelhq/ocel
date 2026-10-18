package filewatch

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	return p.treeOf(path) != ""
}

func (p Paths) treeOf(path string) string {
	for _, dir := range p.Dirs {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return dir
		}
	}
	return ""
}

type Config struct {
	Paths    Paths
	Debounce time.Duration
	SkipDir  func(name string) bool
	OnChange func(changed []string)
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
	if cfg.SkipDir == nil {
		cfg.SkipDir = func(string) bool { return false }
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
	changed := &changes{signal: make(chan struct{}, 1)}
	var handlers sync.WaitGroup
	handlers.Go(func() { handle(ctx, changed, cfg.OnChange) })
	go func() {
		defer close(w.done)
		run(ctx, fsw, cfg, changed)
		handlers.Wait()
	}()
	return w, nil
}

type changes struct {
	mu     sync.Mutex
	paths  []string
	signal chan struct{}
}

func (c *changes) add(paths []string) {
	c.mu.Lock()
	for _, path := range paths {
		if !slices.Contains(c.paths, path) {
			c.paths = append(c.paths, path)
		}
	}
	c.mu.Unlock()
	select {
	case c.signal <- struct{}{}:
	default:
	}
}

func (c *changes) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	taken := c.paths
	c.paths = nil
	return taken
}

func handle(ctx context.Context, changed *changes, onChange func([]string)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed.signal:
			onChange(changed.take())
		}
	}
}

func run(ctx context.Context, fsw *fsnotify.Watcher, cfg Config, changed *changes) {
	defer fsw.Close()

	t := cfg.newTimer(cfg.Debounce)
	if !t.Stop() {
		<-t.C()
	}
	armed := false
	var batch []string

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-fsw.Events:
			if !ok {
				return
			}
			createdDir := event.Has(fsnotify.Create) && isDir(event.Name)
			if isSkipped(cfg, event.Name, createdDir) {
				continue
			}
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				forgetTree(fsw, event.Name)
			}
			if createdDir && cfg.Paths.coversTree(event.Name) {
				watchTree(fsw, cfg, event.Name)
			}
			if !cfg.Paths.covers(event.Name) {
				continue
			}
			batch = append(batch, event.Name)
			if armed && !t.Stop() {
				<-t.C()
			}
			t.Reset(cfg.Debounce)
			armed = true
		case <-t.C():
			armed = false
			changed.add(batch)
			batch = nil
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isSkipped(cfg Config, path string, dir bool) bool {
	if slices.Contains(cfg.Paths.Files, path) {
		return false
	}
	tree := cfg.Paths.treeOf(path)
	if tree == "" {
		return false
	}
	rel, err := filepath.Rel(tree, path)
	if err != nil {
		return false
	}
	segments := strings.Split(rel, string(filepath.Separator))
	if !dir {
		segments = segments[:len(segments)-1]
	}
	return slices.ContainsFunc(segments, cfg.SkipDir)
}

func watchTree(fsw *fsnotify.Watcher, cfg Config, root string) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if path != root && cfg.SkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		_ = fsw.Add(path)
		return nil
	})
}

func forgetTree(fsw *fsnotify.Watcher, root string) {
	for _, watched := range fsw.WatchList() {
		if watched == root || strings.HasPrefix(watched, root+string(filepath.Separator)) {
			_ = fsw.Remove(watched)
		}
	}
}
