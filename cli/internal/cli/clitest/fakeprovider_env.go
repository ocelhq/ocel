package clitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const FakeVarsStoreEnvVar = "OCEL_TEST_FAKE_VARS_STORE"

const FakeRevealFailureEnvVar = "OCEL_TEST_FAKE_REVEAL_FAILURE"

type FakeCell struct {
	Tier       environmentv1.Tier `json:"tier"`
	Coordinate FakeCoordinate     `json:"coordinate"`
	Deleted    bool               `json:"deleted"`
	Versions   []FakeCellData     `json:"versions"`
}

type FakeCoordinate struct {
	Slug        string `json:"slug"`
	Folder      string `json:"folder"`
	Key         string `json:"key"`
	Environment string `json:"environment"`
}

func (c FakeCoordinate) proto() *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: c.Slug, Folder: c.Folder, Key: c.Key, Environment: c.Environment}
}

type FakeCellData struct {
	Value  string          `json:"value"`
	Target *FakeCoordinate `json:"target,omitempty"`
	Ts     int64           `json:"ts"`
}

type FakeStore map[string]*FakeCell

const fakeHistoryWindow = 50

func FakeCoordinateID(tier environmentv1.Tier, c *envvarsv1.Coordinate) string {
	return fmt.Sprintf("%s %q %q %q %q", tier, c.GetSlug(), c.GetFolder(), c.GetKey(), c.GetEnvironment())
}

func LoadFakeStore() (FakeStore, error) {
	store := FakeStore{}
	raw, err := os.ReadFile(os.Getenv(FakeVarsStoreEnvVar))
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	return store, json.Unmarshal(raw, &store)
}

func SaveFakeStore(store FakeStore) error {
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(os.Getenv(FakeVarsStoreEnvVar), raw, 0o600)
}

func (s FakeStore) metadata(tier environmentv1.Tier, c *envvarsv1.Coordinate) *envvarsv1.ValueMetadata {
	return s[FakeCoordinateID(tier, c)].metadata()
}

func (cell *FakeCell) metadata() *envvarsv1.ValueMetadata {
	if cell == nil || cell.Deleted || len(cell.Versions) == 0 {
		return nil
	}
	latest := cell.Versions[len(cell.Versions)-1]
	m := &envvarsv1.ValueMetadata{
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

func (cell *FakeCell) target() *FakeCoordinate {
	if cell == nil || cell.Deleted || len(cell.Versions) == 0 {
		return nil
	}
	return cell.Versions[len(cell.Versions)-1].Target
}

func (s FakeStore) Resolve(tier environmentv1.Tier, cell *FakeCell) (string, error) {
	if target := cell.target(); target != nil {
		held := s[FakeCoordinateID(tier, target.proto())]
		if held.LiveVersion() == 0 {
			return "", connect.NewError(connect.CodeNotFound, fmt.Errorf(
				"%s/%s holds no value: vars: not found", target.Slug, target.Key))
		}
		cell = held
	}
	return cell.Versions[len(cell.Versions)-1].Value, nil
}

func (s FakeStore) referencesTo(tier environmentv1.Tier, at *envvarsv1.Coordinate) []*envvarsv1.Coordinate {
	want := CoordinateOf(at)
	var found []*envvarsv1.Coordinate
	for _, id := range sortedIDs(s) {
		cell := s[id]
		if target := cell.target(); cell.Tier == tier && target != nil && *target == want {
			found = append(found, cell.Coordinate.proto())
		}
	}
	return found
}

func sortedIDs(s FakeStore) []string {
	ids := make([]string, 0, len(s))
	for id := range s {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (c *FakeCell) LiveVersion() int64 {
	if c == nil || c.Deleted {
		return 0
	}
	return int64(len(c.Versions))
}

func checkExpectation(cell *FakeCell, expected *int64) error {
	if expected == nil || *expected == cell.LiveVersion() {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("vars: stale version"))
}

func (s *deployFakeProviderServer) addressable(ctx context.Context, at *envvarsv1.Coordinate, tier environmentv1.Tier) error {
	environment := at.GetEnvironment()
	if environment == "" {
		return nil
	}
	if tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"production has a single environment, so %q addresses no value a production function could read", environment))
	}
	resp, err := s.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: at.GetSlug()})
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
	slices.Sort(names)
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"no preview environment named %q exists, so nothing would ever read that value. This project's environments are: %s",
		environment, strings.Join(names, ", ")))
}

func (s *deployFakeProviderServer) SetValue(ctx context.Context, req *envvarsv1.SetValueRequest) (*envvarsv1.SetValueResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if err := s.addressable(ctx, req.GetCoordinate(), req.GetTier()); err != nil {
		return nil, err
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}

	cell := store[FakeCoordinateID(req.GetTier(), req.GetCoordinate())]
	if err := checkExpectation(cell, req.ExpectedVersion); err != nil {
		return nil, err
	}
	if target := cell.target(); target != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s/%s is a reference to %s/%s, which is where that value is edited: vars: cell is a reference",
			cell.Coordinate.Slug, cell.Coordinate.Key, target.Slug, target.Key))
	}
	if err := store.Write(req.GetTier(), req.GetCoordinate(), FakeCellData{Value: req.GetValue()}); err != nil {
		return nil, err
	}
	return &envvarsv1.SetValueResponse{Metadata: store.metadata(req.GetTier(), req.GetCoordinate())}, nil
}

func (s FakeStore) Write(tier environmentv1.Tier, at *envvarsv1.Coordinate, data FakeCellData) error {
	id := FakeCoordinateID(tier, at)
	cell := s[id]
	if cell == nil {
		cell = &FakeCell{Tier: tier, Coordinate: CoordinateOf(at)}
		s[id] = cell
	}
	data.Ts = 1_700_000_000 + int64(len(cell.Versions))
	cell.Deleted = false
	cell.Versions = append(cell.Versions, data)
	if len(cell.Versions) > fakeHistoryWindow {
		cell.Versions = cell.Versions[len(cell.Versions)-fakeHistoryWindow:]
	}
	return SaveFakeStore(s)
}

func CoordinateOf(c *envvarsv1.Coordinate) FakeCoordinate {
	return FakeCoordinate{Slug: c.GetSlug(), Folder: c.GetFolder(), Key: c.GetKey(), Environment: c.GetEnvironment()}
}

func (s *deployFakeProviderServer) SetReference(ctx context.Context, req *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if err := s.addressable(ctx, req.GetCoordinate(), req.GetTier()); err != nil {
		return nil, err
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}

	at, target := req.GetCoordinate(), req.GetTarget()
	deepens := func(reason string) error {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s: vars: reference to a reference", reason))
	}
	if target.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a reference resolves against %s's class-wide value; %q is an environment of the project holding the reference", target.GetKey(), target.GetEnvironment()))
	}
	if CoordinateOf(at) == CoordinateOf(target) {
		return nil, deepens(describeCoordinate(at) + " would reference itself")
	}

	pointedAt := store[FakeCoordinateID(req.GetTier(), target)]
	if pointedAt.target() != nil {
		return nil, deepens(describeCoordinate(target) + " is itself a reference")
	}
	if consumers := store.referencesTo(req.GetTier(), at); len(consumers) > 0 {
		return nil, deepens(describeCoordinate(at) + " is referenced by " + describeCoordinate(consumers[0]))
	}

	pointer := CoordinateOf(target)
	if err := store.Write(req.GetTier(), at, FakeCellData{Target: &pointer}); err != nil {
		return nil, err
	}
	return &envvarsv1.SetReferenceResponse{Metadata: store.metadata(req.GetTier(), at)}, nil
}

func (s *deployFakeProviderServer) ListReferences(ctx context.Context, req *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}
	return &envvarsv1.ListReferencesResponse{References: store.referencesTo(req.GetTier(), req.GetCoordinate())}, nil
}

func (s *deployFakeProviderServer) ListValues(ctx context.Context, req *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}

	resp := &envvarsv1.ListValuesResponse{}
	for _, id := range sortedIDs(store) {
		cell := store[id]
		if cell.Tier != req.GetTier() || cell.Coordinate.Slug != req.GetSlug() {
			continue
		}
		if m := cell.metadata(); m != nil {
			resp.Values = append(resp.Values, m)
		}
	}
	return resp, nil
}

func (s *deployFakeProviderServer) GetValue(ctx context.Context, req *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}

	metadata := store.metadata(req.GetTier(), req.GetCoordinate())
	if metadata == nil {
		return &envvarsv1.GetValueResponse{}, nil
	}
	resp := &envvarsv1.GetValueResponse{Found: true, Metadata: metadata}
	if req.GetReveal() {
		if resp.Value, err = store.Resolve(req.GetTier(), store[FakeCoordinateID(req.GetTier(), req.GetCoordinate())]); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (s *deployFakeProviderServer) RevealValues(ctx context.Context, req *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if reason := os.Getenv(FakeRevealFailureEnvVar); reason != "" {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(reason))
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}

	resp := &envvarsv1.RevealValuesResponse{}
	for _, cell := range req.GetCells() {
		c := &envvarsv1.Coordinate{Slug: req.GetSlug(), Folder: cell.GetFolder(), Key: cell.GetKey(), Environment: cell.GetEnvironment()}
		metadata := store.metadata(req.GetTier(), c)
		if metadata == nil {
			continue
		}
		value, err := store.Resolve(req.GetTier(), store[FakeCoordinateID(req.GetTier(), c)])
		if err != nil {
			return nil, err
		}
		resp.Values = append(resp.Values, &envvarsv1.RevealedValue{Metadata: metadata, Value: value})
	}
	return resp, nil
}

func (s *deployFakeProviderServer) DeleteValue(ctx context.Context, req *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}

	cell := store[FakeCoordinateID(req.GetTier(), req.GetCoordinate())]
	if err := checkExpectation(cell, req.ExpectedVersion); err != nil {
		return nil, err
	}
	if cell.LiveVersion() == 0 {
		return &envvarsv1.DeleteValueResponse{}, nil
	}
	cell.Deleted = true
	if err := SaveFakeStore(store); err != nil {
		return nil, err
	}
	return &envvarsv1.DeleteValueResponse{Deleted: true}, nil
}

func (s *deployFakeProviderServer) ListVersions(ctx context.Context, req *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}

	cell := store[FakeCoordinateID(req.GetTier(), req.GetCoordinate())]
	if cell == nil {
		return &envvarsv1.ListVersionsResponse{}, nil
	}
	resp := &envvarsv1.ListVersionsResponse{}
	for i := len(cell.Versions) - 1; i >= 0; i-- {
		resp.Versions = append(resp.Versions, &envvarsv1.VersionEntry{
			Version:   int64(i + 1),
			CreatedAt: cell.Versions[i].Ts,
			Size:      int64(len(cell.Versions[i].Value)),
		})
	}
	return resp, nil
}

func describeCoordinate(c *envvarsv1.Coordinate) string {
	out := c.GetSlug() + "/" + c.GetKey()
	if c.GetFolder() != "" {
		out += " in " + c.GetFolder()
	}
	if c.GetEnvironment() != "" {
		out += " for " + c.GetEnvironment()
	}
	return out
}
