package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/dotenv"
)

const (
	DotenvFileName      = ".env"
	LocalDotenvFileName = ".env.local"
)

func ReadDotenv(dir string) (dotenv.File, error) {
	return readDotenvFile(dir, DotenvFileName)
}

func ReadLocalDotenv(dir string) (dotenv.File, error) {
	return readDotenvFile(dir, LocalDotenvFileName)
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

type environment struct {
	dotenv map[string]string
}

func readEnvironment(dir string) (environment, error) {
	file, err := ReadDotenv(dir)
	if err != nil {
		return environment{}, err
	}
	return environment{dotenv: file.Values}, nil
}

func (e environment) lookup(name string) (string, bool) {
	if value, set := os.LookupEnv(name); set {
		return value, true
	}
	value, set := e.dotenv[name]
	return value, set
}

func (e environment) environ() []string {
	merged := os.Environ()
	for key, value := range e.dotenv {
		if _, set := os.LookupEnv(key); set {
			continue
		}
		merged = append(merged, key+"="+value)
	}
	return merged
}

func EnvLookup(dir string) (configdoc.Lookup, error) {
	env, err := readEnvironment(dir)
	if err != nil {
		return nil, err
	}
	return env.lookup, nil
}
