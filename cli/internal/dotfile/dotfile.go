package dotfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/dotenv"
)

const (
	FileName      = ".env"
	LocalFileName = ".env.local"
)

func Load(dir string) (dotenv.File, error) {
	return load(dir, FileName)
}

func LoadLocal(dir string) (dotenv.File, error) {
	return load(dir, LocalFileName)
}

func load(dir, name string) (dotenv.File, error) {
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
