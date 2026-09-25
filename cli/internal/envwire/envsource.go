package envwire

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/localsource"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
)

func Tier(preview bool) environmentv1.Tier {
	if preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func DeployedSource(cfg *projectconfig.Config, preview bool) envsource.Descriptor {
	if preview {
		return cfg.EnvSource.Preview
	}
	return cfg.EnvSource.Production
}

func Folders(cfg *projectconfig.Config) []string {
	folders := []string{""}
	for _, app := range cfg.Apps {
		if !slices.Contains(folders, app.Folder) {
			folders = append(folders, app.Folder)
		}
	}
	slices.Sort(folders)
	return folders
}

func SyncEnvSource(ctx context.Context, runner *provider.Runner, cfg *projectconfig.Config, preview bool) (*envvarsv1.SyncEnvSourceResponse, error) {
	folders := Folders(cfg)
	wire, err := envSourceWire(ctx, DeployedSource(cfg, preview), cfg.Dir, folders)
	if err != nil {
		return nil, err
	}
	vars, err := runner.Vars()
	if err != nil {
		return nil, err
	}
	return vars.SyncEnvSource(ctx, &envvarsv1.SyncEnvSourceRequest{
		Tier:      Tier(preview),
		Slug:      cfg.Slug,
		EnvSource: wire,
		Folders:   folders,
	})
}

func SourceOf(resp *envvarsv1.SyncEnvSourceResponse) envgate.Source {
	status := resp.GetStatus()
	out := StatusSource(status)
	for _, cell := range resp.GetPresent() {
		out.Present = append(out.Present, envgate.Cell{Key: cell.GetKey(), Folder: cell.GetFolder()})
	}
	return out
}

func StatusSource(status *envvarsv1.EnvSourceStatus) envgate.Source {
	out := envgate.Source{ID: status.GetEnvSource(), Writable: status.GetWritable(), Links: map[string]string{}}
	for _, link := range status.GetLinks() {
		out.Links[link.GetFolder()] = link.GetLink()
	}
	return out
}

func envSourceWire(ctx context.Context, descriptor envsource.Descriptor, dir string, folders []string) (*envvarsv1.EnvSource, error) {
	switch descriptor.Kind {
	case envsource.Infisical:
		options := descriptor.Infisical
		held := &envvarsv1.InfisicalEnvSource{
			Project:      options.Project,
			Environment:  options.Environment,
			Path:         options.Path,
			Host:         options.Host,
			WriteMissing: options.Write == envsource.WriteMissing,
			Auth:         &envvarsv1.InfisicalAuth{},
		}
		switch options.Auth.Method {
		case envsource.AuthUniversal:
			held.Auth.Method = &envvarsv1.InfisicalAuth_Universal{Universal: &envvarsv1.InfisicalUniversalAuth{ClientIdVar: options.Auth.ClientIDVar, ClientSecretVar: options.Auth.ClientSecretVar}}
		case envsource.AuthAWS:
			held.Auth.Method = &envvarsv1.InfisicalAuth_Aws{Aws: &envvarsv1.InfisicalIdentityAuth{IdentityId: options.Auth.IdentityID}}
		case envsource.AuthGCP:
			held.Auth.Method = &envvarsv1.InfisicalAuth_Gcp{Gcp: &envvarsv1.InfisicalIdentityAuth{IdentityId: options.Auth.IdentityID}}
		}
		return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Infisical{Infisical: held}}, nil
	case envsource.Exec:
		resolved, err := localsource.Exec(*descriptor.Exec, dir).Resolve(ctx, folders)
		if err != nil {
			return nil, err
		}
		held := &envvarsv1.ExecEnvSource{Command: descriptor.Exec.Command}
		for at, value := range resolved {
			held.Values = append(held.Values, &envvarsv1.SourcedValue{Folder: at.Folder, Key: at.Key, Value: string(value.Value), Version: value.Version})
		}
		slices.SortFunc(held.Values, func(a, b *envvarsv1.SourcedValue) int {
			return cmp.Or(strings.Compare(a.GetFolder(), b.GetFolder()), strings.Compare(a.GetKey(), b.GetKey()))
		})
		return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Exec{Exec: held}}, nil
	}
	return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Builtin{Builtin: &envvarsv1.BuiltinEnvSource{}}}, nil
}
