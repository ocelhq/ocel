package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	connect "connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
)

const envFakeStoreEnvVar = "OCEL_TEST_FAKE_VARS_STORE"

const fakeRevealFailureEnvVar = "OCEL_TEST_FAKE_REVEAL_FAILURE"

type fakeCell struct {
	Class      deploymentsv1.Environment_Class `json:"class"`
	Coordinate fakeCoordinate                  `json:"coordinate"`
	Deleted    bool                            `json:"deleted"`
	Versions   []fakeCellData                  `json:"versions"`
}

type fakeCoordinate struct {
	Slug        string `json:"slug"`
	Folder      string `json:"folder"`
	Key         string `json:"key"`
	Environment string `json:"environment"`
}

func (c fakeCoordinate) proto() *envv1.Coordinate {
	return &envv1.Coordinate{Slug: c.Slug, Folder: c.Folder, Key: c.Key, Environment: c.Environment}
}

type fakeCellData struct {
	Value  string          `json:"value"`
	Target *fakeCoordinate `json:"target,omitempty"`
	Ts     int64           `json:"ts"`
}

type fakeStore map[string]*fakeCell

const fakeHistoryWindow = 50

func fakeCoordinateID(class deploymentsv1.Environment_Class, c *envv1.Coordinate) string {
	return fmt.Sprintf("%s %q %q %q %q", class, c.GetSlug(), c.GetFolder(), c.GetKey(), c.GetEnvironment())
}

func loadFakeStore() (fakeStore, error) {
	store := fakeStore{}
	raw, err := os.ReadFile(os.Getenv(envFakeStoreEnvVar))
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	return store, json.Unmarshal(raw, &store)
}

func saveFakeStore(store fakeStore) error {
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(os.Getenv(envFakeStoreEnvVar), raw, 0o600)
}

func (s fakeStore) metadata(class deploymentsv1.Environment_Class, c *envv1.Coordinate) *envv1.ValueMetadata {
	return s[fakeCoordinateID(class, c)].metadata()
}

func (cell *fakeCell) metadata() *envv1.ValueMetadata {
	if cell == nil || cell.Deleted || len(cell.Versions) == 0 {
		return nil
	}
	latest := cell.Versions[len(cell.Versions)-1]
	m := &envv1.ValueMetadata{
		Coordinate: cell.Coordinate.proto(),
		Version:    int64(len(cell.Versions)),
		UpdatedAt:  latest.Ts,
		Size:       int64(len(latest.Value)),
	}
	if latest.Target != nil {
		m.Target = latest.Target.proto()
	}
	return m
}

func (cell *fakeCell) target() *fakeCoordinate {
	if cell == nil || cell.Deleted || len(cell.Versions) == 0 {
		return nil
	}
	return cell.Versions[len(cell.Versions)-1].Target
}

func (s fakeStore) resolve(class deploymentsv1.Environment_Class, cell *fakeCell) (string, error) {
	if target := cell.target(); target != nil {
		held := s[fakeCoordinateID(class, target.proto())]
		if held.liveVersion() == 0 {
			return "", connect.NewError(connect.CodeNotFound, fmt.Errorf(
				"%s/%s holds no value: no value is set there", target.Slug, target.Key))
		}
		cell = held
	}
	return cell.Versions[len(cell.Versions)-1].Value, nil
}

func (s fakeStore) referencesTo(class deploymentsv1.Environment_Class, at *envv1.Coordinate) []*envv1.Coordinate {
	want := coordinateOf(at)
	var found []*envv1.Coordinate
	for _, id := range sortedIDs(s) {
		cell := s[id]
		if target := cell.target(); cell.Class == class && target != nil && *target == want {
			found = append(found, cell.Coordinate.proto())
		}
	}
	return found
}

func sortedIDs(s fakeStore) []string {
	ids := make([]string, 0, len(s))
	for id := range s {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c *fakeCell) liveVersion() int64 {
	if c == nil || c.Deleted {
		return 0
	}
	return int64(len(c.Versions))
}

func checkExpectation(cell *fakeCell, expected *int64) error {
	if expected == nil || *expected == cell.liveVersion() {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("the value changed since it was read; re-read it and try again"))
}

func (s *deployFakeProviderServer) addressable(ctx context.Context, at *envv1.Coordinate, class deploymentsv1.Environment_Class) error {
	environment := at.GetEnvironment()
	if environment == "" {
		return nil
	}
	if class != deploymentsv1.Environment_CLASS_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"production has a single environment, so %q addresses no value a production function could read", environment))
	}
	resp, err := s.ListEnvironments(ctx, &deploymentsv1.ListEnvironmentsRequest{Slug: at.GetSlug()})
	if err != nil {
		return err
	}
	var names []string
	for _, e := range resp.GetEnvironments() {
		if e.GetIdentity() == environment {
			return nil
		}
		names = append(names, e.GetIdentity())
	}
	if len(names) == 0 {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"no preview environment named %q exists, and this project has none at all; deploy one with `ocel preview` before setting a value only it would read", environment))
	}
	sort.Strings(names)
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"no preview environment named %q exists, so nothing would ever read that value. This project's environments are: %s",
		environment, strings.Join(names, ", ")))
}

func (s *deployFakeProviderServer) SetValue(ctx context.Context, req *envv1.SetValueRequest) (*envv1.SetValueResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if err := s.addressable(ctx, req.GetCoordinate(), req.GetClass()); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	cell := store[fakeCoordinateID(req.GetClass(), req.GetCoordinate())]
	if err := checkExpectation(cell, req.ExpectedVersion); err != nil {
		return nil, err
	}
	if target := cell.target(); target != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s/%s is a reference to %s/%s, which is where that value is edited: that cell is a reference, and a reference has no value of its own",
			cell.Coordinate.Slug, cell.Coordinate.Key, target.Slug, target.Key))
	}
	if err := store.write(req.GetClass(), req.GetCoordinate(), fakeCellData{Value: req.GetValue()}); err != nil {
		return nil, err
	}
	return &envv1.SetValueResponse{Metadata: store.metadata(req.GetClass(), req.GetCoordinate())}, nil
}

func (s fakeStore) write(class deploymentsv1.Environment_Class, at *envv1.Coordinate, data fakeCellData) error {
	id := fakeCoordinateID(class, at)
	cell := s[id]
	if cell == nil {
		cell = &fakeCell{Class: class, Coordinate: coordinateOf(at)}
		s[id] = cell
	}
	data.Ts = 1_700_000_000 + int64(len(cell.Versions))
	cell.Deleted = false
	cell.Versions = append(cell.Versions, data)
	if len(cell.Versions) > fakeHistoryWindow {
		cell.Versions = cell.Versions[len(cell.Versions)-fakeHistoryWindow:]
	}
	return saveFakeStore(s)
}

func coordinateOf(c *envv1.Coordinate) fakeCoordinate {
	return fakeCoordinate{Slug: c.GetSlug(), Folder: c.GetFolder(), Key: c.GetKey(), Environment: c.GetEnvironment()}
}

func (s *deployFakeProviderServer) SetReference(ctx context.Context, req *envv1.SetReferenceRequest) (*envv1.SetReferenceResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if err := s.addressable(ctx, req.GetCoordinate(), req.GetClass()); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	at, target := req.GetCoordinate(), req.GetTarget()
	deepens := func(reason string) error {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s: a reference may only point at a value, never at another reference", reason))
	}
	if target.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a reference resolves against %s's class-wide value; %q is an environment of the project holding the reference", target.GetKey(), target.GetEnvironment()))
	}
	if coordinateOf(at) == coordinateOf(target) {
		return nil, deepens(describeCoordinate(at) + " would reference itself")
	}

	pointedAt := store[fakeCoordinateID(req.GetClass(), target)]
	if pointedAt.target() != nil {
		return nil, deepens(describeCoordinate(target) + " is itself a reference")
	}
	if consumers := store.referencesTo(req.GetClass(), at); len(consumers) > 0 {
		return nil, deepens(describeCoordinate(at) + " is referenced by " + describeCoordinate(consumers[0]))
	}

	cell := store[fakeCoordinateID(req.GetClass(), at)]
	if err := checkExpectation(cell, req.ExpectedVersion); err != nil {
		return nil, err
	}
	pointer := coordinateOf(target)
	if err := store.write(req.GetClass(), at, fakeCellData{Target: &pointer}); err != nil {
		return nil, err
	}
	return &envv1.SetReferenceResponse{Metadata: store.metadata(req.GetClass(), at)}, nil
}

func (s *deployFakeProviderServer) ListReferences(ctx context.Context, req *envv1.ListReferencesRequest) (*envv1.ListReferencesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}
	return &envv1.ListReferencesResponse{References: store.referencesTo(req.GetClass(), req.GetCoordinate())}, nil
}

func (s *deployFakeProviderServer) ListValues(ctx context.Context, req *envv1.ListValuesRequest) (*envv1.ListValuesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	resp := &envv1.ListValuesResponse{}
	for _, id := range sortedIDs(store) {
		cell := store[id]
		if cell.Class != req.GetClass() || cell.Coordinate.Slug != req.GetSlug() {
			continue
		}
		if m := cell.metadata(); m != nil {
			resp.Values = append(resp.Values, m)
		}
	}
	return resp, nil
}

func (s *deployFakeProviderServer) GetValue(ctx context.Context, req *envv1.GetValueRequest) (*envv1.GetValueResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	metadata := store.metadata(req.GetClass(), req.GetCoordinate())
	if metadata == nil {
		return &envv1.GetValueResponse{}, nil
	}
	resp := &envv1.GetValueResponse{Found: true, Metadata: metadata}
	if req.GetReveal() {
		if resp.Value, err = store.resolve(req.GetClass(), store[fakeCoordinateID(req.GetClass(), req.GetCoordinate())]); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (s *deployFakeProviderServer) RevealValues(ctx context.Context, req *envv1.RevealValuesRequest) (*envv1.RevealValuesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if reason := os.Getenv(fakeRevealFailureEnvVar); reason != "" {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(reason))
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	resp := &envv1.RevealValuesResponse{}
	for _, cell := range req.GetCells() {
		c := &envv1.Coordinate{Slug: req.GetSlug(), Folder: cell.GetFolder(), Key: cell.GetKey(), Environment: cell.GetEnvironment()}
		metadata := store.metadata(req.GetClass(), c)
		if metadata == nil {
			continue
		}
		value, err := store.resolve(req.GetClass(), store[fakeCoordinateID(req.GetClass(), c)])
		if err != nil {
			return nil, err
		}
		resp.Values = append(resp.Values, &envv1.RevealedValue{Metadata: metadata, Value: value})
	}
	return resp, nil
}

func (s *deployFakeProviderServer) DeleteValue(ctx context.Context, req *envv1.DeleteValueRequest) (*envv1.DeleteValueResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	cell := store[fakeCoordinateID(req.GetClass(), req.GetCoordinate())]
	if err := checkExpectation(cell, req.ExpectedVersion); err != nil {
		return nil, err
	}
	if cell.liveVersion() == 0 {
		return &envv1.DeleteValueResponse{}, nil
	}
	cell.Deleted = true
	if err := saveFakeStore(store); err != nil {
		return nil, err
	}
	return &envv1.DeleteValueResponse{Deleted: true}, nil
}

func (s *deployFakeProviderServer) ListVersions(ctx context.Context, req *envv1.ListVersionsRequest) (*envv1.ListVersionsResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	cell := store[fakeCoordinateID(req.GetClass(), req.GetCoordinate())]
	if cell == nil {
		return &envv1.ListVersionsResponse{}, nil
	}
	resp := &envv1.ListVersionsResponse{}
	for i := len(cell.Versions) - 1; i >= 0; i-- {
		resp.Versions = append(resp.Versions, &envv1.VersionEntry{
			Version:   int64(i + 1),
			CreatedAt: cell.Versions[i].Ts,
			Size:      int64(len(cell.Versions[i].Value)),
		})
	}
	return resp, nil
}
