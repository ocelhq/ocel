package prerender

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"time"
)

const (
	EntrySegment       = "cache"
	FetchSegment       = "fetch-cache"
	TagSnapshotVersion = 1
	tagSnapshotSuffix  = "/tag-clock.json"
)

type Seed struct {
	Key, Path, Segment string
	Size               int64
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

func Seeds(appRoot, isrPrefix string) ([]Seed, error) {
	var seeds []Seed
	for _, segment := range []string{EntrySegment, FetchSegment} {
		dir := filepath.Join(appRoot, segment)
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
				Key:     path.Join(isrPrefix, segment, filepath.ToSlash(rel)),
				Path:    p,
				Segment: segment,
				Size:    info.Size(),
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

func TagSnapshotKey(isrPrefix string) string { return isrPrefix + tagSnapshotSuffix }
