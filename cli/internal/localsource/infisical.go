package localsource

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func DevInfisical(options envsource.InfisicalOptions, lookupEnv func(string) (string, bool), dir string) (envsource.Source, error) {
	options = options.Normalized()
	options.Write = envsource.WriteNever
	if token, set := lookupEnv(tokenEnvVar); set && strings.TrimSpace(token) != "" {
		return envsource.NewInfisical(options, envsource.AccessToken(strings.TrimSpace(token)), nil), nil
	}
	cli, err := exec.LookPath(infisicalCLI)
	if err != nil {
		return nil, fmt.Errorf("envSource.dev reads Infisical as you, and this shell offers no way in: export %s with an access token, or install the infisical CLI and run `infisical login`", tokenEnvVar)
	}
	return cliSource{options: options, cli: cli, dir: dir}, nil
}

type cliSource struct {
	options envsource.InfisicalOptions
	cli     string
	dir     string
}

func (s cliSource) ID() string { return envsource.InfisicalID(s.options) }

func (cliSource) Capabilities() envsource.Caps { return envsource.Caps{Read: true, List: true} }

func (s cliSource) Resolve(ctx context.Context, folders []string) (map[values.Cell]envsource.Resolved, error) {
	out := map[values.Cell]envsource.Resolved{}
	for _, folder := range folders {
		printed, err := run(ctx, s.dir, []string{
			s.cli, "export",
			"--format=json",
			"--env=" + s.options.Environment,
			"--path=" + path.Join(s.options.Path, "/"+strings.TrimPrefix(folder, "/")),
			"--projectId=" + s.options.Project,
			"--domain=" + s.options.Host,
			"--silent",
		})
		if err != nil {
			return nil, fmt.Errorf("%w — if your infisical login lapsed, run `infisical login` again", err)
		}
		var exported []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
			ID    string `json:"_id"`
		}
		if err := json.Unmarshal(printed, &exported); err != nil {
			return nil, fmt.Errorf("read what `infisical export` printed: %w", err)
		}
		for _, secret := range exported {
			if secret.Value == "" {
				continue
			}
			out[values.Cell{Folder: folder, Key: secret.Key}] = envsource.Resolved{Value: []byte(secret.Value), Version: Version([]byte(secret.Value))}
		}
	}
	return out, nil
}

func (cliSource) Put(context.Context, values.Cell, []byte, string) error {
	return envsource.ErrReadOnly
}

func (cliSource) Link(values.Cell) string { return "" }
