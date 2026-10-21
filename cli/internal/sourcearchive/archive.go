package sourcearchive

import (
	"bufio"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/workspace"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

const (
	MaxFileBytes  = 512 << 20
	MaxTotalBytes = 2 << 30
)

const (
	gitignoreName    = ".gitignore"
	dockerignoreName = ".dockerignore"
	envFilePrefix    = ".env"
	configPrefix     = "ocel."
	cargoOutput      = "target"
	virtualenvMarker = "pyvenv.cfg"
	offendersNamed   = 5
)

var neverUploaded = []string{gitEntry, buildoutput.StateDir, "node_modules", "__pycache__"}

var lockfiles = append(workspace.Lockfiles(), "Cargo.lock", "go.sum", "uv.lock", "poetry.lock", "Pipfile.lock")

type Blob struct {
	Digest string
	Size   uint64
}

type Archive struct {
	Root     string
	Manifest *cloudv1.SourceManifest
	blobs    map[string]blobFile
}

type blobFile struct {
	path string
	size uint64
}

func (a *Archive) Blobs() []Blob {
	blobs := make([]Blob, 0, len(a.blobs))
	for digest, file := range a.blobs {
		blobs = append(blobs, Blob{Digest: digest, Size: file.size})
	}
	slices.SortFunc(blobs, func(x, y Blob) int { return strings.Compare(x.Digest, y.Digest) })
	return blobs
}

func (a *Archive) Open(digest string) (io.ReadCloser, error) {
	file, ok := a.blobs[digest]
	if !ok {
		return nil, fmt.Errorf("the source under %s holds no file with digest %s", a.Root, digest)
	}
	return os.Open(file.path)
}

func Pack(projectDir string) (*Archive, error) {
	root, err := Root(projectDir)
	if err != nil {
		return nil, err
	}
	return Walk(root)
}

type entry struct {
	rel        string
	abs        string
	size       uint64
	executable bool
	symlink    string
}

func Walk(root string) (*Archive, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	docker, err := readDockerignore(root)
	if err != nil {
		return nil, err
	}
	w := walker{root: root, docker: docker}
	patterns, err := readGitExclude(root)
	if err != nil {
		return nil, err
	}
	if err := w.dir(nil, patterns); err != nil {
		return nil, err
	}
	if err := refuseOversized(root, w.entries); err != nil {
		return nil, err
	}
	return digest(root, w.entries)
}

type walker struct {
	root    string
	docker  *patternmatcher.PatternMatcher
	entries []entry
}

func (w *walker) dir(segments []string, inherited []gitignore.Pattern) error {
	abs := filepath.Join(append([]string{w.root}, segments...)...)
	patterns, err := readGitignore(filepath.Join(abs, gitignoreName), segments)
	if err != nil {
		return err
	}
	patterns = append(slices.Clip(inherited), patterns...)
	ignored := gitignore.NewMatcher(patterns)

	listed, err := os.ReadDir(abs)
	if err != nil {
		return fmt.Errorf("read %s: %w", abs, err)
	}
	besideCargoManifest := slices.ContainsFunc(listed, func(listing os.DirEntry) bool { return listing.Name() == cargoManifest })
	for _, listing := range listed {
		name := listing.Name()
		if slices.Contains(neverUploaded, name) {
			continue
		}
		child := append(slices.Clip(segments), name)
		rel := path.Join(child...)
		childAbs := filepath.Join(abs, name)
		info, err := os.Lstat(childAbs)
		if err != nil {
			return fmt.Errorf("read %s: %w", childAbs, err)
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if isToolOutput(childAbs, name, besideCargoManifest) || ignored.Match(child, true) || w.dockerExcludes(rel, true) {
				continue
			}
			if err := w.dir(child, patterns); err != nil {
				return err
			}
		case mode.IsRegular():
			if strings.HasPrefix(name, envFilePrefix) {
				continue
			}
			if !alwaysUploaded(name) && (ignored.Match(child, false) || w.dockerExcludes(rel, false)) {
				continue
			}
			w.entries = append(w.entries, entry{rel: rel, abs: childAbs, size: uint64(info.Size()), executable: mode&0o111 != 0})
		case mode&fs.ModeSymlink != 0:
			if ignored.Match(child, false) || w.dockerExcludes(rel, false) {
				continue
			}
			target, err := w.symlinkTarget(rel, childAbs)
			if err != nil {
				return err
			}
			w.entries = append(w.entries, entry{rel: rel, abs: childAbs, symlink: target})
		}
	}
	return nil
}

func (w *walker) dockerExcludes(rel string, isDir bool) bool {
	if w.docker == nil {
		return false
	}
	if isDir && w.docker.Exclusions() {
		return false
	}
	excluded, err := w.docker.MatchesOrParentMatches(rel)
	return err == nil && excluded
}

func (w *walker) symlinkTarget(rel, abs string) (string, error) {
	target, err := os.Readlink(abs)
	if err != nil {
		return "", fmt.Errorf("read the symlink %s: %w", abs, err)
	}
	if filepath.IsAbs(target) {
		inside, err := filepath.Rel(filepath.Dir(abs), target)
		if err != nil {
			return "", fmt.Errorf("the symlink %s points at %s, outside %s: replace it with the file, or ignore it in %s", rel, target, w.root, gitignoreName)
		}
		target = inside
	}
	target = filepath.ToSlash(target)
	resolved := path.Join(path.Dir(rel), target)
	if resolved == ".." || strings.HasPrefix(resolved, "../") || target == "" {
		return "", fmt.Errorf("the symlink %s points at %s, outside %s: replace it with the file, or ignore it in %s", rel, target, w.root, gitignoreName)
	}
	return target, nil
}

func isToolOutput(dir, name string, besideCargoManifest bool) bool {
	if name == cargoOutput && besideCargoManifest {
		return true
	}
	_, err := os.Lstat(filepath.Join(dir, virtualenvMarker))
	return err == nil
}

func alwaysUploaded(name string) bool {
	return strings.HasPrefix(name, configPrefix) || slices.Contains(lockfiles, name)
}

func readGitignore(file string, domain []string) ([]gitignore.Pattern, error) {
	opened, err := os.Open(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	defer func() { _ = opened.Close() }()
	var patterns []gitignore.Pattern
	lines := bufio.NewScanner(opened)
	for lines.Scan() {
		line := strings.TrimSuffix(lines.Text(), "\r")
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
	}
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return patterns, nil
}

func readGitExclude(root string) ([]gitignore.Pattern, error) {
	return readGitignore(filepath.Join(root, gitEntry, "info", "exclude"), nil)
}

func readDockerignore(root string) (*patternmatcher.PatternMatcher, error) {
	file := filepath.Join(root, dockerignoreName)
	opened, err := os.Open(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	defer func() { _ = opened.Close() }()
	patterns, err := ignorefile.ReadAll(opened)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return matcher, nil
}

func refuseOversized(root string, entries []entry) error {
	var total uint64
	var oversized []entry
	for _, e := range entries {
		total += e.size
		if e.size > MaxFileBytes {
			oversized = append(oversized, e)
		}
	}
	if len(oversized) > 0 {
		return fmt.Errorf("%s %s over the %s a source file may be: ignore %s in %s or %s",
			english.And(named(biggest(oversized))), english.Number(len(oversized), "is", "are"), english.Bytes(MaxFileBytes),
			english.Number(len(oversized), "it", "them"), gitignoreName, dockerignoreName)
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("the source under %s is %s, over the %s an upload may be; the biggest files are %s: ignore what the build does not need in %s or %s",
			root, english.Bytes(total), english.Bytes(MaxTotalBytes), english.And(named(biggest(entries))), gitignoreName, dockerignoreName)
	}
	return nil
}

func biggest(entries []entry) []entry {
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(x, y entry) int { return cmp.Or(cmp.Compare(y.size, x.size), strings.Compare(x.rel, y.rel)) })
	return sorted[:min(len(sorted), offendersNamed)]
}

func named(entries []entry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, fmt.Sprintf("%s (%s)", e.rel, english.Bytes(e.size)))
	}
	return names
}

func digest(root string, entries []entry) (*Archive, error) {
	archive := &Archive{Root: root, Manifest: &cloudv1.SourceManifest{}, blobs: map[string]blobFile{}}
	for _, e := range entries {
		file := &cloudv1.SourceFile{Path: e.rel, SymlinkTarget: e.symlink}
		if e.symlink == "" {
			sum, size, err := hashFile(e.abs)
			if err != nil {
				return nil, err
			}
			file.Digest, file.Size, file.Executable = sum, size, e.executable
			archive.blobs[sum] = blobFile{path: e.abs, size: size}
		}
		archive.Manifest.Files = append(archive.Manifest.Files, file)
	}
	slices.SortFunc(archive.Manifest.Files, func(x, y *cloudv1.SourceFile) int { return strings.Compare(x.GetPath(), y.GetPath()) })
	return archive, nil
}

func hashFile(file string) (string, uint64, error) {
	opened, err := os.Open(file)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", file, err)
	}
	defer func() { _ = opened.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(opened, MaxFileBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", file, err)
	}
	if size > MaxFileBytes {
		return "", 0, fmt.Errorf("%s grew past the %s a source file may be while it was read", file, english.Bytes(MaxFileBytes))
	}
	return hex.EncodeToString(hash.Sum(nil)), uint64(size), nil
}
