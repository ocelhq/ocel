package valuestore

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/envsource"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func (s Store) DescribeEnvSource(ctx context.Context) (variables.EnvSource, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return variables.EnvSource{}, err
	}
	resp, err := variableStore.DescribeEnvSource(ctx, &variablestorev1.DescribeEnvSourceRequest{Tier: s.Tier, Slug: s.Project.Slug})
	if err != nil {
		return variables.EnvSource{}, err
	}
	return EnvSourceOfStatus(resp.GetStatus()), nil
}

func (s Store) SyncEnvSource(ctx context.Context) (variables.EnvSource, error) {
	descriptor := variablescope.EnvSourceDescriptor(s.Project, s.Tier)
	folders := syncedFolders(s.Project)
	var read map[variablestore.Cell]envsource.Value
	if descriptor.Reading() == envsource.ReadingWhereOcelRuns {
		source, err := descriptor.Open(s.Project.Dir, os.LookupEnv)
		if err != nil {
			return variables.EnvSource{}, err
		}
		if read, err = source.Read(ctx, folders); err != nil {
			return variables.EnvSource{}, err
		}
	}
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return variables.EnvSource{}, err
	}
	resp, err := variableStore.SyncEnvSource(ctx, &variablestorev1.SyncEnvSourceRequest{
		Tier:    s.Tier,
		Slug:    s.Project.Slug,
		From:    &variablestorev1.SyncEnvSourceRequest_EnvSource{EnvSource: envSourceMessage(descriptor, read)},
		Folders: folders,
	})
	if err != nil {
		return variables.EnvSource{}, err
	}
	return envSourceOf(resp), nil
}

func envSourceMessage(descriptor envsource.Descriptor, read map[variablestore.Cell]envsource.Value) *variablestorev1.EnvSource {
	sent := &variablestorev1.EnvSource{Kind: descriptor.Kind(), Options: descriptor.Options()}
	for _, at := range slices.SortedFunc(maps.Keys(read), variablestore.Cell.Compare) {
		sent.Values = append(sent.Values, &variablestorev1.EnvSourceValue{
			Cell:  &variablestorev1.Cell{Folder: at.Folder, Key: at.Key},
			Value: string(read[at].Plaintext),
		})
	}
	return sent
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

func (s Store) SyncRegisteredEnvSource(ctx context.Context) (*variablestorev1.SyncEnvSourceResponse, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return nil, err
	}
	return variableStore.SyncEnvSource(ctx, &variablestorev1.SyncEnvSourceRequest{
		Tier: s.Tier,
		Slug: s.Project.Slug,
		From: &variablestorev1.SyncEnvSourceRequest_Registered{Registered: &variablestorev1.RegisteredEnvSource{}},
	})
}

func (s Store) SetInEnvSource(ctx context.Context, at variables.Cell, value, description string) (bool, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return false, err
	}
	resp, err := variableStore.SetEnvSourceValue(ctx, &variablestorev1.SetEnvSourceValueRequest{
		Tier:        s.Tier,
		Coordinate:  &variablestorev1.Coordinate{Slug: s.Project.Slug, Folder: at.Folder, Key: at.Key},
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
		refused, ok := value.(*variablestorev1.CredentialRefusal)
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

func envSourceOf(resp *variablestorev1.SyncEnvSourceResponse) variables.EnvSource {
	out := EnvSourceOfStatus(resp.GetStatus())
	for _, cell := range resp.GetPresent() {
		out.Present = append(out.Present, variables.Cell{Key: cell.GetKey(), Folder: cell.GetFolder()})
	}
	return out
}

func EnvSourceOfStatus(status *variablestorev1.EnvSourceStatus) variables.EnvSource {
	out := variables.EnvSource{ID: status.GetEnvSource(), CanCreate: status.GetCanCreate(), CanUpdate: status.GetCanUpdate(), Credentials: status.GetCredentials()}
	for _, link := range status.GetLinks() {
		if out.URLs == nil {
			out.URLs = map[string]string{}
		}
		out.URLs[link.GetFolder()] = link.GetUrl()
	}
	return out
}
