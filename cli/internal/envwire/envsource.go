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
	"github.com/ocelhq/ocel/cli/internal/varsui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
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

const CredentialGroup = "env source"

func CredentialDefinitions(cfg *projectconfig.Config, preview bool) []*resourcesv1.VariableDefinition {
	descriptor := DeployedSource(cfg, preview)
	if descriptor.Infisical == nil {
		return nil
	}
	var out []*resourcesv1.VariableDefinition
	for _, name := range descriptor.Infisical.Auth.Vars() {
		out = append(out, &resourcesv1.VariableDefinition{
			Key:         name,
			Class:       resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
			Required:    true,
			Group:       CredentialGroup,
			Description: "read by ocel alone, never by an app",
		})
	}
	return out
}

func declareCredentials(ctx context.Context, gate *envgate.Gate, cfg *projectconfig.Config, preview bool) error {
	declared := gate.Definitions()
	var owed []*resourcesv1.VariableDefinition
	for _, definition := range CredentialDefinitions(cfg, preview) {
		if !slices.ContainsFunc(declared, func(held *resourcesv1.VariableDefinition) bool { return held.GetKey() == definition.GetKey() }) {
			owed = append(owed, definition)
		}
	}
	if len(owed) == 0 {
		return nil
	}
	req := &resourcesv1.DeclareEnvRequest{Definitions: owed}
	if !slices.ContainsFunc(gate.Groups(), func(held *resourcesv1.GroupDefinition) bool { return held.GetKey() == CredentialGroup }) {
		req.Groups = []*resourcesv1.GroupDefinition{{Key: CredentialGroup, Required: true, Description: "how ocel signs in to " + DeployedSource(cfg, preview).ID()}}
	}
	_, err := gate.DeclareEnv(ctx, req)
	return err
}

type EnvSourceValues struct {
	Runner  *provider.Runner
	Config  *projectconfig.Config
	Preview bool
}

func (e EnvSourceValues) Describe(ctx context.Context) (varsui.EnvSource, error) {
	vars, err := e.Runner.Vars()
	if err != nil {
		return varsui.EnvSource{}, err
	}
	resp, err := vars.DescribeEnvSource(ctx, &envvarsv1.DescribeEnvSourceRequest{Tier: Tier(e.Preview), Slug: e.Config.Slug})
	if err != nil {
		return varsui.EnvSource{}, err
	}
	source := StatusSource(resp.GetStatus())
	out := varsui.EnvSource{ID: source.ID, Writable: source.Writable, Links: source.Links}
	for _, definition := range CredentialDefinitions(e.Config, e.Preview) {
		out.Credentials = append(out.Credentials, definition.GetKey())
	}
	return out, nil
}

func (e EnvSourceValues) Sync(ctx context.Context) error {
	_, err := SyncEnvSource(ctx, e.Runner, e.Config, e.Preview)
	return err
}

func (e EnvSourceValues) Create(ctx context.Context, at envgate.Address, value, description string) (bool, error) {
	vars, err := e.Runner.Vars()
	if err != nil {
		return false, err
	}
	resp, err := vars.PutEnvSourceValue(ctx, &envvarsv1.PutEnvSourceValueRequest{
		Tier:        Tier(e.Preview),
		Coordinate:  &envvarsv1.Coordinate{Slug: e.Config.Slug, Folder: at.Cell.Folder, Key: at.Cell.Key},
		Value:       value,
		Description: description,
	})
	if err != nil {
		return false, err
	}
	return resp.GetAwaitingApproval(), nil
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
