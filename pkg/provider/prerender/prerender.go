package prerender

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	TagSnapshotVersion = 1
	tagSnapshotFile    = "tag-clock.json"
)

var seedDirs = []struct {
	dir  string
	kind naming.ObjectKind
}{
	{dir: "cache", kind: naming.ObjectPages},
	{dir: "fetch-cache", kind: naming.ObjectFetchCache},
}

type Seed struct {
	Key, Path string
	Kind      naming.ObjectKind
	Size      int64
}

type TagSnapshot struct {
	Version     int                  `json:"version"`
	DeployedAt  int64                `json:"deployedAt"`
	GeneratedAt int64                `json:"generatedAt"`
	Records     map[string]TagRecord `json:"records"`
}

type TagRecord struct {
	Stale   int64 `json:"stale,omitempty"`
	Expired int64 `json:"expired,omitempty"`
}

func Seeds(appRoot string, release naming.Coordinate) ([]Seed, error) {
	var seeds []Seed
	for _, seeded := range seedDirs {
		dir := filepath.Join(appRoot, seeded.dir)
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			seeds = append(seeds, Seed{
				Key:  release.ObjectKey(seeded.kind, filepath.ToSlash(rel)),
				Path: p,
				Kind: seeded.kind,
				Size: info.Size(),
			})
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("crawl %s: %w", dir, err)
		}
	}
	return seeds, nil
}

func GenesisTagSnapshot(at time.Time) TagSnapshot {
	ms := at.UnixMilli()
	return TagSnapshot{
		Version:     TagSnapshotVersion,
		DeployedAt:  ms,
		GeneratedAt: ms,
		Records:     map[string]TagRecord{},
	}
}

func TagSnapshotKey(release naming.Coordinate) string {
	return release.ObjectKey(naming.ObjectTagClock, tagSnapshotFile)
}
