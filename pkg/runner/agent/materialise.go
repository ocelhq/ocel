package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/runner"
)

const maxLinkDepth = 40

func Materialise(ctx context.Context, dir string, source *runnerv1.SourceManifest, open func(ctx context.Context, digest string) (io.ReadCloser, error)) error {
	files := source.GetFiles()
	links := map[string]string{}
	var total uint64
	for _, file := range files {
		if err := checkPath(file.GetPath()); err != nil {
			return err
		}
		if file.GetSize() > runner.MaxSourceBytes-total {
			return errors.New("the source holds more than 2 GiB, the most a source may hold")
		}
		total += file.GetSize()
		if file.GetSymlinkTarget() != "" {
			links[file.GetPath()] = file.GetSymlinkTarget()
		}
	}
	resolve := &resolver{ctx: ctx, links: links, landed: map[string]landing{}}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		for parent := path.Dir(file.GetPath()); parent != "."; parent = path.Dir(parent) {
			if _, ok := links[parent]; ok {
				return fmt.Errorf("%s sits under %s, which is a symlink: a source lists no file beneath one", file.GetPath(), parent)
			}
		}
		if target := file.GetSymlinkTarget(); target != "" {
			if _, _, err := resolve.link(file.GetPath()); err != nil {
				return fmt.Errorf("the symlink %s → %s %w", file.GetPath(), target, err)
			}
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range files {
		if file.GetSymlinkTarget() != "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeFile(ctx, root, file, open); err != nil {
			return fmt.Errorf("write %s: %w", file.GetPath(), err)
		}
	}
	for _, file := range files {
		if target := file.GetSymlinkTarget(); target != "" {
			if parent := path.Dir(file.GetPath()); parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return err
				}
			}
			if err := root.Symlink(target, file.GetPath()); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeFile(ctx context.Context, root *os.Root, file *runnerv1.SourceFile, open func(ctx context.Context, digest string) (io.ReadCloser, error)) error {
	if parent := path.Dir(file.GetPath()); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	mode := os.FileMode(0o644)
	if file.GetExecutable() {
		mode = 0o755
	}
	body, err := open(ctx, file.GetDigest())
	if err != nil {
		return err
	}
	defer body.Close()
	out, err := root.OpenFile(file.GetPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(body, int64(file.GetSize())+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if uint64(written) != file.GetSize() {
		return fmt.Errorf("its blob holds %d bytes or more, and the source says %d", written, file.GetSize())
	}
	if sum := hex.EncodeToString(hash.Sum(nil)); sum != file.GetDigest() {
		return fmt.Errorf("its blob hashes to %s, not %s", sum, file.GetDigest())
	}
	return root.Chmod(file.GetPath(), mode)
}

func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\") {
		return fmt.Errorf("the source path %q is not relative and slash-separated", p)
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("the source path %q leaves the source or names no file", p)
		}
		for _, r := range segment {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("the source path %q holds a control character", p)
			}
		}
	}
	return nil
}

var (
	errLeaves  = errors.New("leaves the source")
	errTooDeep = fmt.Errorf("passes through more than %d symlinks", maxLinkDepth)
)

type resolver struct {
	ctx    context.Context
	links  map[string]string
	landed map[string]landing
}

type landing struct {
	at   string
	hops int
	err  error
}

func (r *resolver) follow(dir, target string) (string, int, error) {
	if strings.HasPrefix(target, "/") {
		return "", 0, errLeaves
	}
	var at []string
	if dir != "." {
		at = strings.Split(dir, "/")
	}
	hops := 0
	for _, segment := range strings.Split(target, "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			if len(at) == 0 {
				return "", 0, errLeaves
			}
			at = at[:len(at)-1]
			continue
		}
		at = append(at, segment)
		name := strings.Join(at, "/")
		if _, ok := r.links[name]; !ok {
			continue
		}
		landed, through, err := r.link(name)
		if err != nil {
			return "", 0, err
		}
		if hops += through; hops > maxLinkDepth {
			return "", 0, errTooDeep
		}
		at = nil
		if landed != "." {
			at = strings.Split(landed, "/")
		}
	}
	if len(at) == 0 {
		return ".", hops, nil
	}
	return strings.Join(at, "/"), hops, nil
}

func (r *resolver) link(name string) (string, int, error) {
	if landed, ok := r.landed[name]; ok {
		return landed.at, landed.hops, landed.err
	}
	if err := r.ctx.Err(); err != nil {
		return "", 0, err
	}
	r.landed[name] = landing{hops: maxLinkDepth + 1}
	at, hops, err := r.follow(path.Dir(name), r.links[name])
	if hops++; err == nil && hops > maxLinkDepth {
		err = errTooDeep
	}
	r.landed[name] = landing{at: at, hops: hops, err: err}
	return at, hops, err
}
