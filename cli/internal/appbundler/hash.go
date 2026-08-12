package appbundler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const buildIDLength = 16

type hashedFile struct {
	rel string
	sum string
}

func artifactHash(dir string) (string, error) {
	var files []hashedFile
	walkErr := filepath.WalkDir(dir, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, current)
		if err != nil {
			return err
		}
		sum, err := fileHash(current)
		if err != nil {
			return err
		}
		files = append(files, hashedFile{rel: filepath.ToSlash(rel), sum: sum})
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("hash %s: %w", dir, walkErr)
	}

	slices.SortFunc(files, func(a, b hashedFile) int { return strings.Compare(a.rel, b.rel) })
	digest := sha256.New()
	for _, file := range files {
		fmt.Fprintf(digest, "%s\x00%s\n", file.rel, file.sum)
	}
	return hex.EncodeToString(digest.Sum(nil))[:buildIDLength], nil
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
