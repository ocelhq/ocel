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

	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
)

// envFakeStoreEnvVar points the fake provider at the JSON file its variable
// store lives in. A provider process is spawned per command, so the store has
// to outlive one: without a file on disk, a value set by one command could not
// be read back by the next, and the round trip the store exists for would be
// untestable at the command level.
const envFakeStoreEnvVar = "OCEL_TEST_FAKE_VARS_STORE"

// fakeCell is one coordinate's stored state in the fake store: its version
// history, newest last.
type fakeCell struct {
	Deleted  bool           `json:"deleted"`
	Versions []fakeCellData `json:"versions"`
}

type fakeCellData struct {
	Value string `json:"value"`
	Ts    int64  `json:"ts"`
}

// fakeStore mirrors the real store's observable contract — versions, a
// retention window, reveal-gated plaintext, and current-pointer deletion —
// without any of its cloud machinery, so a command test asserts on what an
// operator sees rather than on how the provider stored it.
type fakeStore map[string]*fakeCell

const fakeHistoryWindow = 50

func fakeCoordinateID(c *envv1.Coordinate) string {
	return fmt.Sprintf("%s|%s|%s|%s", c.GetSlug(), c.GetFolder(), c.GetKey(), c.GetEnvironment())
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

func (s fakeStore) metadata(c *envv1.Coordinate) *envv1.ValueMetadata {
	cell := s[fakeCoordinateID(c)]
	if cell == nil || cell.Deleted || len(cell.Versions) == 0 {
		return nil
	}
	latest := cell.Versions[len(cell.Versions)-1]
	return &envv1.ValueMetadata{
		Coordinate: c,
		Version:    int64(len(cell.Versions)),
		UpdatedAt:  latest.Ts,
		Size:       int64(len(latest.Value)),
	}
}

// liveVersion is the version a reader can observe, zero when the cell holds no
// value — the same rule the real store applies, and what an expectation is
// compared against.
func (c *fakeCell) liveVersion() int64 {
	if c == nil || c.Deleted {
		return 0
	}
	return int64(len(c.Versions))
}

// checkExpectation is the provider half of optimistic concurrency: an
// expectation that no longer describes the cell is FAILED_PRECONDITION, which
// is the one code the wire contract reserves for it.
func checkExpectation(cell *fakeCell, expected *int64) error {
	if expected == nil || *expected == cell.liveVersion() {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("the value changed since it was read; re-read it and try again"))
}

func (s *deployFakeProviderServer) SetValue(ctx context.Context, req *envv1.SetValueRequest) (*envv1.SetValueResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	id := fakeCoordinateID(req.GetCoordinate())
	cell := store[id]
	if err := checkExpectation(cell, req.ExpectedVersion); err != nil {
		return nil, err
	}
	if cell == nil {
		cell = &fakeCell{}
		store[id] = cell
	}

	cell.Deleted = false
	cell.Versions = append(cell.Versions, fakeCellData{Value: req.GetValue(), Ts: 1_700_000_000 + int64(len(cell.Versions))})
	if len(cell.Versions) > fakeHistoryWindow {
		cell.Versions = cell.Versions[len(cell.Versions)-fakeHistoryWindow:]
	}
	if err := saveFakeStore(store); err != nil {
		return nil, err
	}
	return &envv1.SetValueResponse{Metadata: store.metadata(req.GetCoordinate())}, nil
}

func (s *deployFakeProviderServer) ListValues(ctx context.Context, req *envv1.ListValuesRequest) (*envv1.ListValuesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(store))
	for id := range store {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	resp := &envv1.ListValuesResponse{}
	for _, id := range ids {
		parts := strings.SplitN(id, "|", 4)
		if parts[0] != req.GetSlug() {
			continue
		}
		c := &envv1.Coordinate{Slug: parts[0], Folder: parts[1], Key: parts[2], Environment: parts[3]}
		if m := store.metadata(c); m != nil {
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

	metadata := store.metadata(req.GetCoordinate())
	if metadata == nil {
		return &envv1.GetValueResponse{}, nil
	}
	resp := &envv1.GetValueResponse{Found: true, Metadata: metadata}
	if req.GetReveal() {
		versions := store[fakeCoordinateID(req.GetCoordinate())].Versions
		resp.Value = versions[len(versions)-1].Value
	}
	return resp, nil
}

func (s *deployFakeProviderServer) RevealValues(ctx context.Context, req *envv1.RevealValuesRequest) (*envv1.RevealValuesResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeStore()
	if err != nil {
		return nil, err
	}

	resp := &envv1.RevealValuesResponse{}
	for _, c := range req.GetCoordinates() {
		c.Slug = req.GetSlug()
		metadata := store.metadata(c)
		if metadata == nil {
			continue
		}
		versions := store[fakeCoordinateID(c)].Versions
		resp.Values = append(resp.Values, &envv1.RevealedValue{
			Metadata: metadata,
			Value:    versions[len(versions)-1].Value,
		})
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

	cell := store[fakeCoordinateID(req.GetCoordinate())]
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

	cell := store[fakeCoordinateID(req.GetCoordinate())]
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
