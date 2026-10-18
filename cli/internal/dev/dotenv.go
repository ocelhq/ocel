package dev

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/dotenv"
)

func readDotenv(dir string) (dotenv.File, error) {
	return readDotenvFile(dir, project.DotenvFileName)
}

func readLocalDotenv(dir string) (dotenv.File, error) {
	return readDotenvFile(dir, project.LocalDotenvFileName)
}

func readDotenvFile(dir, name string) (dotenv.File, error) {
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dotenv.File{Values: map[string]string{}}, nil
		}
		return dotenv.File{}, fmt.Errorf("read %s: %w", name, err)
	}
	defer file.Close()

	parsed, err := dotenv.Parse(file)
	if err != nil {
		return dotenv.File{}, fmt.Errorf("read %s: %w", name, err)
	}
	return parsed, nil
}
