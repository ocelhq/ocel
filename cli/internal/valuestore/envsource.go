package valuestore

import (
	"context"
	"errors"
	"os"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envsourcewire"
	"github.com/ocelhq/ocel/pkg/envvars"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func (s Store) DescribeEnvSource(ctx context.Context) (variables.EnvSource, error) {
	vars, err := s.Provider.Vars()
	if err != nil {
		return variables.EnvSource{}, err
	}
	resp, err := vars.DescribeEnvSource(ctx, &envvarsv1.DescribeEnvSourceRequest{Tier: s.Tier, Slug: s.Project.Slug})
	if err != nil {
		return variables.EnvSource{}, err
	}
	return EnvSourceOfStatus(resp.GetStatus()), nil
}

func (s Store) SyncEnvSource(ctx context.Context) (variables.EnvSource, error) {
	descriptor := variablescope.EnvSourceDescriptor(s.Project, s.Tier)
	folders := syncedFolders(s.Project)
	var read map[envvars.Cell]envsource.Value
	if descriptor.Kind == envsource.Exec {
		source, err := envsource.Open(descriptor, s.Project.Dir, os.LookupEnv)
		if err != nil {
			return variables.EnvSource{}, err
		}
		if read, err = source.Read(ctx, folders); err != nil {
			return variables.EnvSource{}, err
		}
	}
	vars, err := s.Provider.Vars()
	if err != nil {
		return variables.EnvSource{}, err
	}
	resp, err := vars.SyncEnvSource(ctx, &envvarsv1.SyncEnvSourceRequest{
		Tier:    s.Tier,
		Slug:    s.Project.Slug,
		From:    &envvarsv1.SyncEnvSourceRequest_EnvSource{EnvSource: envsourcewire.Encode(descriptor, read)},
		Folders: folders,
	})
	if err != nil {
		return variables.EnvSource{}, err
	}
	return envSourceOf(resp), nil
}

func syncedFolders(cfg *project.Project) []string {
	folders := []string{""}
	for _, app := range cfg.Apps {
		if !slices.Contains(folders, app.Folder) {
			folders = append(folders, app.Folder)
		}
	}
	slices.Sort(folders)
	return folders
}

func (s Store) SyncRegisteredEnvSource(ctx context.Context) (*envvarsv1.SyncEnvSourceResponse, error) {
	vars, err := s.Provider.Vars()
	if err != nil {
		return nil, err
	}
	return vars.SyncEnvSource(ctx, &envvarsv1.SyncEnvSourceRequest{
		Tier: s.Tier,
		Slug: s.Project.Slug,
		From: &envvarsv1.SyncEnvSourceRequest_Registered{Registered: &envvarsv1.RegisteredEnvSource{}},
	})
}

func (s Store) SetInEnvSource(ctx context.Context, at variables.Cell, value, description string) (bool, error) {
	vars, err := s.Provider.Vars()
	if err != nil {
		return false, err
	}
	resp, err := vars.SetEnvSourceValue(ctx, &envvarsv1.SetEnvSourceValueRequest{
		Tier:        s.Tier,
		Coordinate:  &envvarsv1.Coordinate{Slug: s.Project.Slug, Folder: at.Folder, Key: at.Key},
		Value:       value,
		Description: description,
	})
	if err != nil {
		return false, err
	}
	return resp.GetAwaitingApproval(), nil
}

func CredentialProblems(err error) []*resourcesv1.VariableProblem {
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return nil
	}
	var out []*resourcesv1.VariableProblem
	for _, detail := range wire.Details() {
		value, err := detail.Value()
		if err != nil {
			continue
		}
		refused, ok := value.(*envvarsv1.CredentialRefusal)
		if !ok {
			continue
		}
		problem := &resourcesv1.VariableProblem{Key: refused.GetVariable(), Kind: resourcesv1.VariableProblem_KIND_MISSING}
		if !refused.GetUnset() {
			problem.Kind, problem.Detail = resourcesv1.VariableProblem_KIND_INVALID, refused.GetReason()
		}
		out = append(out, problem)
	}
	return out
}

func envSourceOf(resp *envvarsv1.SyncEnvSourceResponse) variables.EnvSource {
	out := EnvSourceOfStatus(resp.GetStatus())
	for _, cell := range resp.GetPresent() {
		out.Present = append(out.Present, variables.Cell{Key: cell.GetKey(), Folder: cell.GetFolder()})
	}
	return out
}

func EnvSourceOfStatus(status *envvarsv1.EnvSourceStatus) variables.EnvSource {
	out := variables.EnvSource{ID: status.GetEnvSource(), CanCreate: status.GetCanCreate(), CanUpdate: status.GetCanUpdate(), Credentials: status.GetCredentials()}
	for _, link := range status.GetLinks() {
		if out.URLs == nil {
			out.URLs = map[string]string{}
		}
		out.URLs[link.GetFolder()] = link.GetUrl()
	}
	return out
}
