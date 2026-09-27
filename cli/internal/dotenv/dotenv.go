package dotenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/dotenv"
)

const FileName = ".env"

func Load(dir string) (dotenv.File, error) {
	file, err := os.Open(filepath.Join(dir, FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dotenv.File{Values: map[string]string{}}, nil
		}
		return dotenv.File{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	defer file.Close()

	parsed, err := dotenv.Parse(file)
	if err != nil {
		return dotenv.File{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	return parsed, nil
}
