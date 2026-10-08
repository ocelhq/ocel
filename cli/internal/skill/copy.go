//go:build ignore

package main

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

const (
	source      = "../../../skills/ocel"
	destination = "dist"
)

func main() {
	if err := os.RemoveAll(destination); err != nil {
		log.Fatal(err)
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		log.Fatal(err)
	}
}
