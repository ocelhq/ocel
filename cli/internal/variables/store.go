package variables

import (
	"context"
	"errors"
	"fmt"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Store struct {
	Provider        *providerprocess.Provider
	Project         *project.Project
	Tier            environmentv1.Tier
	DefinitionsOnly bool
}

func (s Store) List(ctx context.Context) ([]ValueMetadata, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return nil, err
	}
	resp, err := variableStore.ListValues(ctx, &variablestorev1.ListValuesRequest{
		Tier: s.Tier,
		Slug: s.Project.Slug,
	})
	if err != nil {
		return nil, err
	}

	var stored []ValueMetadata
	for _, value := range resp.GetValues() {
		stored = append(stored, ValueMetadata{
			Coordinate: coordinateOf(value.GetCoordinate()),
			Version:    value.GetVersion(),
			Reference:  referenceOf(value.GetTarget()),
			EnvSource:  value.GetEnvSource(),

			Publisher:       value.GetPublisher(),
			PublishedSecret: value.GetPublishedSecret(),
		})
	}
	return stored, nil
}

func coordinateOf(c *variablestorev1.Coordinate) Coordinate {
	return Coordinate{Cell: Cell{Key: c.GetKey(), Folder: c.GetFolder()}, Environment: c.GetEnvironment()}
}

func referenceOf(target *variablestorev1.Coordinate) *Reference {
	if target == nil {
		return nil
	}
	return &Reference{Slug: target.GetSlug(), Folder: target.GetFolder(), Key: target.GetKey()}
}

func (s Store) Reveal(ctx context.Context, rows []Coordinate) (map[Coordinate]string, error) {
	if s.DefinitionsOnly {
		return map[Coordinate]string{}, nil
	}
	named := make([]*variablestorev1.Coordinate, 0, len(rows))
	for _, row := range rows {
		named = append(named, s.coordinate(row))
	}
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return nil, err
	}
	resp, err := variableStore.RevealValues(ctx, &variablestorev1.RevealValuesRequest{
		Tier:  s.Tier,
		Slug:  s.Project.Slug,
		Cells: named,
	})
	if err != nil {
		return nil, err
	}
	found := make(map[Coordinate]string, len(resp.GetValues()))
	for _, value := range resp.GetValues() {
		found[coordinateOf(value.GetMetadata().GetCoordinate())] = value.GetValue()
	}
	return found, nil
}

func (s Store) coordinate(at Coordinate) *variablestorev1.Coordinate {
	return &variablestorev1.Coordinate{Slug: s.Project.Slug, Folder: at.Cell.Folder, Key: at.Cell.Key, Environment: at.Environment}
}

func (s Store) Version(ctx context.Context, at Coordinate) (int64, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return 0, err
	}
	resp, err := variableStore.GetValue(ctx, &variablestorev1.GetValueRequest{
		Tier:       s.Tier,
		Coordinate: s.coordinate(at),
	})
	if err != nil {
		return 0, err
	}
	return resp.GetMetadata().GetVersion(), nil
}

type ValueWrite struct {
	At       Coordinate
	Value    string
	Expected *int64
}

func (s Store) Set(ctx context.Context, writes []ValueWrite) ([]int64, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return nil, err
	}
	values := make([]*variablestorev1.ValueWrite, 0, len(writes))
	for _, write := range writes {
		values = append(values, &variablestorev1.ValueWrite{Coordinate: s.coordinate(write.At), Value: write.Value, ExpectedVersion: write.Expected})
	}
	resp, err := variableStore.SetValues(ctx, &variablestorev1.SetValuesRequest{
		Tier:   s.Tier,
		Slug:   s.Project.Slug,
		Values: values,
	})
	if err != nil {
		return nil, staleValueError(err)
	}
	versions := make([]int64, 0, len(resp.GetWritten()))
	for _, written := range resp.GetWritten() {
		versions = append(versions, written.GetVersion())
	}
	return versions, nil
}

func (s Store) Delete(ctx context.Context, at Coordinate, expected *int64) (bool, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return false, err
	}
	resp, err := variableStore.DeleteValue(ctx, &variablestorev1.DeleteValueRequest{
		Tier:            s.Tier,
		Coordinate:      s.coordinate(at),
		ExpectedVersion: expected,
	})
	if err != nil {
		return false, staleValueError(err)
	}
	return resp.GetDeleted(), nil
}

func staleValueError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeAborted {
		return err
	}
	if _, refused := provider.RefusedCode(err); refused {
		return err
	}
	var conflict *connect.Error
	if errors.As(err, &conflict) {
		return fmt.Errorf("%w: %s", ErrStaleValue, conflict.Message())
	}
	return ErrStaleValue
}

func (s Store) History(ctx context.Context, at Coordinate) ([]Version, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return nil, err
	}
	resp, err := variableStore.ListVersions(ctx, &variablestorev1.ListVersionsRequest{
		Tier:       s.Tier,
		Coordinate: s.coordinate(at),
	})
	if err != nil {
		return nil, err
	}

	versions := make([]Version, 0, len(resp.GetVersions()))
	for _, entry := range resp.GetVersions() {
		versions = append(versions, Version{
			Version:   entry.GetVersion(),
			CreatedAt: entry.GetCreatedAt(),
			Size:      entry.GetSize(),
		})
	}
	return versions, nil
}

func ListEnvironmentNames(ctx context.Context, opened *providerprocess.Provider, slug string) ([]string, error) {
	var resp *contractv1.ListEnvironmentsResponse
	err := opened.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
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

func ReadDeployedDeclarations(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, env *environmentv1.Environment) (*Declarations, error) {
	store := Store{Provider: provider, Project: cfg, Tier: env.GetTier()}
	scope := ScopeOf(cfg, env.GetTier(), env.GetIdentity())
	described, err := store.DescribeEnvSource(ctx)
	if err != nil {
		return nil, err
	}
	scope.EnvSource = described
	return NewDeclarations(store, scope), nil
}
