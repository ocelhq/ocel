package envwire

import (
	"context"
	"errors"
	"fmt"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	"github.com/ocelhq/ocel/cli/node"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const RootApp = "this project's app"

func ServeVarsUI(ctx context.Context, cfg *projectconfig.Config, runner *provider.Runner, preview bool, gate *envgate.Gate, recovery *varsui.Recovery) (*varsui.Session, error) {
	assets, err := node.VarsUI()
	if err != nil {
		return nil, fmt.Errorf("read the bundled variables UI: %w", err)
	}

	tier, other := environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW
	if preview {
		tier, other = other, tier
	}
	store := Values{
		Runner: runner,
		Slug:   cfg.Slug,
		Tier:   tier,
	}

	var environments []string
	if preview {
		var err error
		if environments, err = NamedEnvironments(ctx, runner, cfg.Slug); err != nil {
			return nil, err
		}
	}

	return varsui.Serve(ctx, varsui.Options{
		Assets:       assets,
		Gate:         gate,
		Store:        store,
		Other:        Values{Runner: runner, Slug: cfg.Slug, Tier: other},
		Slug:         cfg.Slug,
		Preview:      preview,
		Environments: environments,
		Recovery:     recovery,
	})
}

func NamedEnvironments(ctx context.Context, runner *provider.Runner, slug string) ([]string, error) {
	client, err := runner.Client()
	if err != nil {
		return nil, err
	}
	resp, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{
		Slug: slug,
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

func Scope(cfg *projectconfig.Config, preview bool, environment string) envgate.Scope {
	return envgate.Scope{Apps: Apps(cfg), Preview: preview, Environment: environment}
}

func Apps(cfg *projectconfig.Config) []envgate.App {
	if len(cfg.Apps) == 0 {
		return []envgate.App{{Name: RootApp}}
	}
	apps := make([]envgate.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		apps = append(apps, envgate.App{Name: a.Name, Folder: a.Folder})
	}
	return apps
}

type Values struct {
	Runner *provider.Runner
	Slug   string
	Tier   environmentv1.Tier
}

func (v Values) List(ctx context.Context) ([]envgate.Stored, error) {
	vars, err := v.Runner.Vars()
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

	var stored []envgate.Stored
	for _, value := range resp.GetValues() {
		c := value.GetCoordinate()
		stored = append(stored, envgate.Stored{
			Address: envgate.Address{
				Cell:        envgate.Cell{Key: c.GetKey(), Folder: c.GetFolder()},
				Environment: c.GetEnvironment(),
			},
			Version:   value.GetVersion(),
			Reference: referenceOf(value.GetTarget()),
		})
	}
	return stored, nil
}

func referenceOf(target *envvarsv1.Coordinate) *envgate.Reference {
	if target == nil {
		return nil
	}
	return &envgate.Reference{Slug: target.GetSlug(), Folder: target.GetFolder(), Key: target.GetKey()}
}

func (v Values) Read(ctx context.Context, rows []envgate.Address) (map[envgate.Address]string, error) {
	resp, err := v.reveal(ctx, rows)
	if err != nil {
		return nil, err
	}
	found := make(map[envgate.Address]string, len(resp.GetValues()))
	for _, value := range resp.GetValues() {
		c := value.GetMetadata().GetCoordinate()
		found[envgate.Address{Cell: envgate.Cell{Key: c.GetKey(), Folder: c.GetFolder()}, Environment: c.GetEnvironment()}] = value.GetValue()
	}
	return found, nil
}

func (v Values) reveal(ctx context.Context, rows []envgate.Address) (*envvarsv1.RevealValuesResponse, error) {
	named := make([]*envvarsv1.Coordinate, 0, len(rows))
	for _, row := range rows {
		named = append(named, v.coordinate(row))
	}
	vars, err := v.Runner.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.RevealValues(ctx, &envvarsv1.RevealValuesRequest{
		Tier:  v.Tier,
		Slug:  v.Slug,
		Cells: named,
	})
	if err != nil {
		return nil, errors.New(err.Error())
	}
	return resp, nil
}

func (v Values) Reveal(ctx context.Context, rows []envgate.Address) (map[envgate.Cell]string, error) {
	resp, err := v.reveal(ctx, rows)
	if err != nil {
		return nil, err
	}

	found := make(map[envgate.Cell]string, len(resp.GetValues()))
	for _, value := range resp.GetValues() {
		c := value.GetMetadata().GetCoordinate()
		found[envgate.Cell{Key: c.GetKey(), Folder: c.GetFolder()}] = value.GetValue()
	}
	return found, nil
}

func (v Values) coordinate(at envgate.Address) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: v.Slug, Folder: at.Cell.Folder, Key: at.Cell.Key, Environment: at.Environment}
}

func (v Values) Set(ctx context.Context, at envgate.Address, value string, expected *int64) error {
	vars, err := v.Runner.Vars()
	if err != nil {
		return err
	}
	_, err = vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:            v.Tier,
		Coordinate:      v.coordinate(at),
		Value:           value,
		ExpectedVersion: expected,
	})
	return staleOrBroken(err)
}

func (v Values) Delete(ctx context.Context, at envgate.Address, expected *int64) error {
	vars, err := v.Runner.Vars()
	if err != nil {
		return err
	}
	_, err = vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{
		Tier:            v.Tier,
		Coordinate:      v.coordinate(at),
		ExpectedVersion: expected,
	})
	return staleOrBroken(err)
}

func staleOrBroken(err error) error {
	if err != nil && connect.CodeOf(err) == connect.CodeFailedPrecondition {
		return varsui.ErrStaleValue
	}
	return err
}

func (v Values) History(ctx context.Context, at envgate.Address) ([]varsui.Version, error) {
	vars, err := v.Runner.Vars()
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
