package valuestore

import (
	"context"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/variables"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Store struct {
	Provider *providerprocess.Provider
	Project  *project.Project
	Tier     environmentv1.Tier
}

func (s Store) List(ctx context.Context) ([]variables.ValueMetadata, error) {
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

	var stored []variables.ValueMetadata
	for _, value := range resp.GetValues() {
		stored = append(stored, variables.ValueMetadata{
			Coordinate: coordinateOf(value.GetCoordinate()),
			Version:    value.GetVersion(),
			Reference:  referenceOf(value.GetTarget()),
			EnvSource:  value.GetEnvSource(),
		})
	}
	return stored, nil
}

func coordinateOf(c *variablestorev1.Coordinate) variables.Coordinate {
	return variables.Coordinate{Cell: variables.Cell{Key: c.GetKey(), Folder: c.GetFolder()}, Environment: c.GetEnvironment()}
}

func referenceOf(target *variablestorev1.Coordinate) *variables.Reference {
	if target == nil {
		return nil
	}
	return &variables.Reference{Slug: target.GetSlug(), Folder: target.GetFolder(), Key: target.GetKey()}
}

func (s Store) Reveal(ctx context.Context, rows []variables.Coordinate) (map[variables.Coordinate]string, error) {
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
	found := make(map[variables.Coordinate]string, len(resp.GetValues()))
	for _, value := range resp.GetValues() {
		found[coordinateOf(value.GetMetadata().GetCoordinate())] = value.GetValue()
	}
	return found, nil
}

func (s Store) coordinate(at variables.Coordinate) *variablestorev1.Coordinate {
	return &variablestorev1.Coordinate{Slug: s.Project.Slug, Folder: at.Cell.Folder, Key: at.Cell.Key, Environment: at.Environment}
}

func (s Store) Version(ctx context.Context, at variables.Coordinate) (int64, error) {
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

func (s Store) Set(ctx context.Context, at variables.Coordinate, value string, expected *int64) (int64, error) {
	variableStore, err := s.Provider.VariableStore()
	if err != nil {
		return 0, err
	}
	resp, err := variableStore.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:            s.Tier,
		Coordinate:      s.coordinate(at),
		Value:           value,
		ExpectedVersion: expected,
	})
	if err != nil {
		return 0, staleValueError(err)
	}
	return resp.GetMetadata().GetVersion(), nil
}

func (s Store) Delete(ctx context.Context, at variables.Coordinate, expected *int64) (bool, error) {
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
	return variables.ErrStaleValue
}

func (s Store) History(ctx context.Context, at variables.Coordinate) ([]variables.Version, error) {
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

	versions := make([]variables.Version, 0, len(resp.GetVersions()))
	for _, entry := range resp.GetVersions() {
		versions = append(versions, variables.Version{
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
