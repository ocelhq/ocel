package envsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/ocelhq/ocel/pkg/envvars"
)

const infisicalCLIName = "infisical"

type infisicalExport struct {
	options InfisicalOptions
	cli     string
	dir     string
}

func (s infisicalExport) ID() string { return s.options.ID() }

func (s infisicalExport) Read(ctx context.Context, folders []string) (map[envvars.Cell]Value, error) {
	out := map[envvars.Cell]Value{}
	for _, folder := range folders {
		at, err := s.options.secretPath(folder)
		if err != nil {
			return nil, err
		}
		printed, err := runCommand(ctx, s.dir, []string{
			s.cli, "export",
			"--format=json",
			"--env=" + s.options.Environment,
			"--path=" + at,
			"--projectId=" + s.options.Project,
			"--domain=" + s.options.Host,
			"--silent",
		})
		if folder != "" && isMissingFolder(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w; if your infisical login lapsed, run `infisical login` again", err)
		}
		var exported []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if json.Unmarshal(printed, &exported) != nil {
			return nil, fmt.Errorf("`infisical export` printed something other than a JSON list of secrets for %s", at)
		}
		for _, secret := range exported {
			if secret.Value == "" {
				continue
			}
			out[envvars.Cell{Folder: folder, Key: secret.Key}] = Value{Plaintext: []byte(secret.Value)}
		}
	}
	return out, nil
}

var infisicalNotFound = regexp.MustCompile(`Response Code: 404\b|\[status-code=404\]`)

func isMissingFolder(err error) bool {
	var failed *commandFailure
	return errors.As(err, &failed) && infisicalNotFound.MatchString(failed.stderr)
}

func (infisicalExport) Create(context.Context, envvars.Cell, []byte, string) error {
	return ErrReadOnly
}

func (infisicalExport) Update(context.Context, envvars.Cell, []byte, string) error {
	return ErrReadOnly
}

func (infisicalExport) URL(envvars.Cell) string { return "" }
