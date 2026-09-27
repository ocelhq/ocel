package envwire

import (
	"context"
	"os"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envsourcewire"
	"github.com/ocelhq/ocel/pkg/envvars"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func DeployedEnvSource(cfg *projectconfig.Config, preview bool) envsource.Descriptor {
	if preview {
		return cfg.EnvSource.Preview
	}
	return cfg.EnvSource.Production
}

func configuredEnvSource(cfg *projectconfig.Config, preview bool) envgate.EnvSource {
	descriptor := DeployedEnvSource(cfg, preview)
	out := envgate.EnvSource{ID: descriptor.ID()}
	if descriptor.Infisical != nil {
		out.Credentials = descriptor.Infisical.Auth.Variables()
	}
	return out
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

func SyncEnvSource(ctx context.Context, runner *providerclient.Runner, cfg *projectconfig.Config, preview bool) (*envvarsv1.SyncEnvSourceResponse, error) {
	descriptor := DeployedEnvSource(cfg, preview)
	folders := Folders(cfg)
	var read map[envvars.Cell]envsource.Value
	if descriptor.Kind == envsource.Exec {
		source, err := envsource.Open(descriptor, cfg.Dir, os.LookupEnv)
		if err != nil {
			return nil, err
		}
		if read, err = source.Read(ctx, folders); err != nil {
			return nil, err
		}
	}
	vars, err := runner.Vars()
	if err != nil {
		return nil, err
	}
	return vars.SyncEnvSource(ctx, &envvarsv1.SyncEnvSourceRequest{
		Tier:    tierOf(preview),
		Slug:    cfg.Slug,
		From:    &envvarsv1.SyncEnvSourceRequest_EnvSource{EnvSource: envsourcewire.Encode(descriptor, read)},
		Folders: folders,
	})
}

func SyncRegisteredEnvSource(ctx context.Context, runner *providerclient.Runner, slug string, preview bool) (*envvarsv1.SyncEnvSourceResponse, error) {
	vars, err := runner.Vars()
	if err != nil {
		return nil, err
	}
	return vars.SyncEnvSource(ctx, &envvarsv1.SyncEnvSourceRequest{
		Tier: tierOf(preview),
		Slug: slug,
		From: &envvarsv1.SyncEnvSourceRequest_Registered{Registered: &envvarsv1.RegisteredEnvSource{}},
	})
}

func EnvSourceOf(resp *envvarsv1.SyncEnvSourceResponse) envgate.EnvSource {
	out := EnvSourceOfStatus(resp.GetStatus())
	for _, cell := range resp.GetPresent() {
		out.Present = append(out.Present, envgate.Cell{Key: cell.GetKey(), Folder: cell.GetFolder()})
	}
	return out
}

func EnvSourceOfStatus(status *envvarsv1.EnvSourceStatus) envgate.EnvSource {
	out := envgate.EnvSource{ID: status.GetEnvSource(), Writable: status.GetWritable(), Credentials: status.GetCredentials()}
	for _, link := range status.GetLinks() {
		if out.URLs == nil {
			out.URLs = map[string]string{}
		}
		out.URLs[link.GetFolder()] = link.GetUrl()
	}
	return out
}

func tierOf(preview bool) environmentv1.Tier {
	if preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}
