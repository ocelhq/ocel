package envwire

import (
	"context"
	"fmt"
	"path/filepath"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/appbuild"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const RootApp = "this project's app"

func ServeVarsUI(ctx context.Context, cfg *projectconfig.Config, prov *providerclient.Provider, preview bool, gate *variables.Declarations, recovery *varsui.Recovery) (*varsui.Session, error) {
	assets, err := node.VarsUI()
	if err != nil {
		return nil, fmt.Errorf("read the bundled variables UI: %w", err)
	}

	tier, other := environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW
	if preview {
		tier, other = other, tier
	}
	store := Values{
		Provider: prov,
		Slug:     cfg.Slug,
		Tier:     tier,
	}

	var environments []string
	if preview {
		var err error
		if environments, err = NamedEnvironments(ctx, prov, cfg.Slug); err != nil {
			return nil, err
		}
	}

	return varsui.Serve(ctx, varsui.Options{
		Assets:       assets,
		Gate:         gate,
		Store:        store,
		Other:        Values{Provider: prov, Slug: cfg.Slug, Tier: other},
		Slug:         cfg.Slug,
		Preview:      preview,
		Environments: environments,
		Recovery:     recovery,
		EnvSource:    EnvSourceClient{Provider: prov, Config: cfg, Preview: preview},
	})
}

func NamedEnvironments(ctx context.Context, prov *providerclient.Provider, slug string) ([]string, error) {
	var resp *contractv1.ListEnvironmentsResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		var err error
		resp, err = client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{
			Slug: slug,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.GetEnvironments()))
	for _, environment := range resp.GetEnvironments() {
		names = append(names, environment.GetIdentity())
	}
	return names, nil
}

func Scope(cfg *projectconfig.Config, preview bool, environment string) variables.Scope {
	tier, other := environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW
	if preview {
		tier, other = other, tier
	}
	return variables.Scope{
		Apps:        Apps(cfg),
		Tier:        tier,
		Environment: environment,
		Bindings:    BindingVariables(cfg, tier),
		OtherTiers:  BindingVariables(cfg, other),
		EnvSource:   ConfiguredEnvSource(cfg, preview),
	}
}

func BindingVariables(cfg *projectconfig.Config, tier environmentv1.Tier) []variables.BindingVariables {
	var out []variables.BindingVariables
	for _, binding := range cfg.BindingsFor(tier) {
		if binding.Inline == nil {
			continue
		}
		out = append(out, variables.BindingVariables{
			Group: binding.Group(),
			Site:  "bindings." + binding.Group(),
			Keys:  binding.Inline.Variables(),
		})
	}
	return out
}

func DevScope(cfg *projectconfig.Config) variables.Scope {
	return variables.Scope{Apps: Apps(cfg)}
}

func Apps(cfg *projectconfig.Config) []variables.App {
	if len(cfg.Apps) == 0 {
		return []variables.App{{Name: RootApp, ClientBundle: discovery.ClientBundle(appbuild.FrameworkNode, cfg.Dir)}}
	}
	apps := make([]variables.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		apps = append(apps, variables.App{
			Name:         a.Name,
			Folder:       a.Folder,
			ClientBundle: discovery.ClientBundle(a.Framework.Name, filepath.Join(cfg.Dir, a.Path)),
		})
	}
	return apps
}

type Values struct {
	Provider *providerclient.Provider
	Slug     string
	Tier     environmentv1.Tier
}

func (v Values) List(ctx context.Context) ([]variables.ValueMetadata, error) {
	vars, err := v.Provider.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.ListValues(ctx, &envvarsv1.ListValuesRequest{
		Tier: v.Tier,
		Slug: v.Slug,
	})
	if err != nil {
		return nil, err
	}

	var stored []variables.ValueMetadata
	for _, value := range resp.GetValues() {
		c := value.GetCoordinate()
		stored = append(stored, variables.ValueMetadata{
			Coordinate: variables.Coordinate{
				Cell:        variables.Cell{Key: c.GetKey(), Folder: c.GetFolder()},
				Environment: c.GetEnvironment(),
			},
			Version:   value.GetVersion(),
			Reference: referenceOf(value.GetTarget()),
			EnvSource: value.GetEnvSource(),
		})
	}
	return stored, nil
}

func referenceOf(target *envvarsv1.Coordinate) *variables.Reference {
	if target == nil {
		return nil
	}
	return &variables.Reference{Slug: target.GetSlug(), Folder: target.GetFolder(), Key: target.GetKey()}
}

func (v Values) Read(ctx context.Context, rows []variables.Coordinate) (map[variables.Coordinate]string, error) {
	resp, err := v.reveal(ctx, rows)
	if err != nil {
		return nil, err
	}
	found := make(map[variables.Coordinate]string, len(resp.GetValues()))
	for _, value := range resp.GetValues() {
		c := value.GetMetadata().GetCoordinate()
		found[variables.Coordinate{Cell: variables.Cell{Key: c.GetKey(), Folder: c.GetFolder()}, Environment: c.GetEnvironment()}] = value.GetValue()
	}
	return found, nil
}

func (v Values) reveal(ctx context.Context, rows []variables.Coordinate) (*envvarsv1.RevealValuesResponse, error) {
	named := make([]*envvarsv1.Coordinate, 0, len(rows))
	for _, row := range rows {
		named = append(named, v.coordinate(row))
	}
	vars, err := v.Provider.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.RevealValues(ctx, &envvarsv1.RevealValuesRequest{
		Tier:  v.Tier,
		Slug:  v.Slug,
		Cells: named,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (v Values) Reveal(ctx context.Context, rows []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return v.Read(ctx, rows)
}

func (v Values) coordinate(at variables.Coordinate) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: v.Slug, Folder: at.Cell.Folder, Key: at.Cell.Key, Environment: at.Environment}
}

func (v Values) Version(ctx context.Context, at variables.Coordinate) (int64, error) {
	vars, err := v.Provider.Vars()
	if err != nil {
		return 0, err
	}
	resp, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{
		Tier:       v.Tier,
		Coordinate: v.coordinate(at),
	})
	if err != nil {
		return 0, err
	}
	return resp.GetMetadata().GetVersion(), nil
}

func (v Values) Write(ctx context.Context, at variables.Coordinate, value string, expected *int64) (*envvarsv1.ValueMetadata, error) {
	vars, err := v.Provider.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:            v.Tier,
		Coordinate:      v.coordinate(at),
		Value:           value,
		ExpectedVersion: expected,
	})
	if err != nil {
		return nil, staleOrBroken(err)
	}
	return resp.GetMetadata(), nil
}

func (v Values) Remove(ctx context.Context, at variables.Coordinate, expected *int64) (bool, error) {
	vars, err := v.Provider.Vars()
	if err != nil {
		return false, err
	}
	resp, err := vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{
		Tier:            v.Tier,
		Coordinate:      v.coordinate(at),
		ExpectedVersion: expected,
	})
	if err != nil {
		return false, staleOrBroken(err)
	}
	return resp.GetDeleted(), nil
}

func (v Values) Set(ctx context.Context, at variables.Coordinate, value string, expected *int64) error {
	_, err := v.Write(ctx, at, value, expected)
	return err
}

func (v Values) Delete(ctx context.Context, at variables.Coordinate, expected *int64) error {
	_, err := v.Remove(ctx, at, expected)
	return err
}

func staleOrBroken(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeAborted {
		return err
	}
	if _, refused := provider.RefusedCode(err); refused {
		return err
	}
	return varsui.ErrStaleValue
}

func (v Values) History(ctx context.Context, at variables.Coordinate) ([]varsui.Version, error) {
	vars, err := v.Provider.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.ListVersions(ctx, &envvarsv1.ListVersionsRequest{
		Tier:       v.Tier,
		Coordinate: v.coordinate(at),
	})
	if err != nil {
		return nil, err
	}

	versions := make([]varsui.Version, 0, len(resp.GetVersions()))
	for _, entry := range resp.GetVersions() {
		versions = append(versions, varsui.Version{
			Version:   entry.GetVersion(),
			CreatedAt: entry.GetCreatedAt(),
			Size:      entry.GetSize(),
		})
	}
	return versions, nil
}
