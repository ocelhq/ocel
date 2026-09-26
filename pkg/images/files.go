package images

import (
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
)

func ArtifactFiles(dir string) ([]string, error) {
	var rels []string
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() && entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return nil, err
	}
	slices.Sort(rels)
	return rels, nil
}

func OverlayFiles(overlay map[string][]byte) []string {
	return slices.Sorted(maps.Keys(overlay))
}
