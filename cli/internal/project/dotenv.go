package project

import (
	"os"

	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

type environment struct {
	dotenv map[string]string
}

func readEnvironment(dir string) (environment, error) {
	file, err := dotfile.Load(dir)
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
