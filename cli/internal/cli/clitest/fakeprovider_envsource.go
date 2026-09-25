package clitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const FakeEnvSourceEnvVar = "OCEL_TEST_FAKE_ENV_SOURCE"

type FakeEnvSource struct {
	ID       string            `json:"id"`
	Values   []FakeSourced     `json:"values"`
	Links    map[string]string `json:"links"`
	Error    string            `json:"error"`
	Attempt  int64             `json:"attempt"`
	Success  int64             `json:"success"`
	LastSeen string            `json:"lastError"`
}

type FakeSourced struct {
	Folder string `json:"folder"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

type FakeRegistration struct {
	ID          string            `json:"id"`
	Standing    bool              `json:"standing"`
	Writable    bool              `json:"writable"`
	Credentials []string          `json:"credentials"`
	Links       map[string]string `json:"links"`
	Attempt     int64             `json:"attempt"`
	Success     int64             `json:"success"`
	LastError   string            `json:"lastError"`
	Folders     []string          `json:"folders"`
	Created     []FakeSourced     `json:"created"`
}

type FakeRegistrations map[string]FakeRegistration

func fakeRegistrationsPath() string { return os.Getenv(FakeVarsStoreEnvVar) + ".sources" }

func LoadFakeRegistrations() (FakeRegistrations, error) {
	held := FakeRegistrations{}
	raw, err := os.ReadFile(fakeRegistrationsPath())
	if errors.Is(err, os.ErrNotExist) {
		return held, nil
	}
	if err != nil {
		return nil, err
	}
	return held, json.Unmarshal(raw, &held)
}

func saveFakeRegistrations(held FakeRegistrations) error {
	raw, err := json.Marshal(held)
	if err != nil {
		return err
	}
	return os.WriteFile(fakeRegistrationsPath(), raw, 0o600)
}

func registrationKey(tier environmentv1.Tier, slug string) string { return tier.String() + " " + slug }

func fakeSource() (FakeEnvSource, error) {
	var source FakeEnvSource
	raw := os.Getenv(FakeEnvSourceEnvVar)
	if raw == "" {
		return source, errors.New("the fake provider was handed no env source to read")
	}
	return source, json.Unmarshal([]byte(raw), &source)
}

func (s *deployFakeProviderServer) SyncEnvSource(_ context.Context, req *envvarsv1.SyncEnvSourceRequest) (*envvarsv1.SyncEnvSourceResponse, error) {
	registrations, err := LoadFakeRegistrations()
	if err != nil {
		return nil, err
	}
	key := registrationKey(req.GetTier(), req.GetSlug())
	wire := req.GetEnvSource()
	var registration FakeRegistration
	var values []FakeSourced
	switch {
	case wire.GetInfisical() != nil:
		source, err := fakeSource()
		if err != nil {
			return nil, err
		}
		if source.Error != "" {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New(source.Error))
		}
		held := wire.GetInfisical()
		registration = FakeRegistration{ID: source.ID, Standing: true, Writable: held.GetWriteMissing(), Links: source.Links, Attempt: source.Attempt, Success: source.Success, LastError: source.LastSeen}
		if universal := held.GetAuth().GetUniversal(); universal != nil {
			registration.Credentials = []string{universal.GetClientIdVar(), universal.GetClientSecretVar()}
		}
		values = source.Values
	case wire.GetExec() != nil:
		registration = FakeRegistration{ID: "exec", Attempt: 1_700_000_100, Success: 1_700_000_100}
		for _, value := range wire.GetExec().GetValues() {
			values = append(values, FakeSourced{Folder: value.GetFolder(), Key: value.GetKey(), Value: value.GetValue()})
		}
	default:
		delete(registrations, key)
		if err := saveFakeRegistrations(registrations); err != nil {
			return nil, err
		}
		return &envvarsv1.SyncEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: "builtin"}}, nil
	}
	registration.Folders = req.GetFolders()
	registrations[key] = registration
	if err := saveFakeRegistrations(registrations); err != nil {
		return nil, err
	}

	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}
	resp := &envvarsv1.SyncEnvSourceResponse{Status: registration.status()}
	for _, value := range values {
		if !slices.Contains(req.GetFolders(), value.Folder) {
			continue
		}
		resp.Present = append(resp.Present, &envvarsv1.SourcedCell{Folder: value.Folder, Key: value.Key})
		at := &envvarsv1.Coordinate{Slug: req.GetSlug(), Folder: value.Folder, Key: value.Key}
		held := store[FakeCoordinateID(req.GetTier(), at)]
		if held.LiveVersion() > 0 {
			latest := held.Versions[len(held.Versions)-1]
			if latest.Value == value.Value && latest.EnvSource == registration.ID {
				continue
			}
		}
		if err := store.Write(req.GetTier(), at, FakeCellData{Value: value.Value, EnvSource: registration.ID}); err != nil {
			return nil, err
		}
		resp.Written++
	}
	return resp, nil
}

func (r FakeRegistration) status() *envvarsv1.EnvSourceStatus {
	status := &envvarsv1.EnvSourceStatus{
		EnvSource:     r.ID,
		Standing:      r.Standing,
		Writable:      r.Writable,
		LastAttemptAt: r.Attempt,
		LastSuccessAt: r.Success,
		LastError:     r.LastError,
		Credentials:   r.Credentials,
	}
	folders := make([]string, 0, len(r.Links))
	for folder := range r.Links {
		folders = append(folders, folder)
	}
	slices.Sort(folders)
	for _, folder := range folders {
		status.Links = append(status.Links, &envvarsv1.FolderLink{Folder: folder, Link: r.Links[folder]})
	}
	return status
}

func (s *deployFakeProviderServer) DescribeEnvSource(_ context.Context, req *envvarsv1.DescribeEnvSourceRequest) (*envvarsv1.DescribeEnvSourceResponse, error) {
	registrations, err := LoadFakeRegistrations()
	if err != nil {
		return nil, err
	}
	registration, held := registrations[registrationKey(req.GetTier(), req.GetSlug())]
	if !held {
		return &envvarsv1.DescribeEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: "builtin"}}, nil
	}
	return &envvarsv1.DescribeEnvSourceResponse{Status: registration.status()}, nil
}

func (s *deployFakeProviderServer) PutEnvSourceValue(_ context.Context, req *envvarsv1.PutEnvSourceValueRequest) (*envvarsv1.PutEnvSourceValueResponse, error) {
	registrations, err := LoadFakeRegistrations()
	if err != nil {
		return nil, err
	}
	at := req.GetCoordinate()
	key := registrationKey(req.GetTier(), at.GetSlug())
	registration, held := registrations[key]
	if !held || !registration.Writable {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s reads from a source ocel may not write into", at.GetKey()))
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}
	if store[FakeCoordinateID(req.GetTier(), at)].LiveVersion() > 0 {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("%s already holds %s", registration.ID, at.GetKey()))
	}
	registration.Created = append(registration.Created, FakeSourced{Folder: at.GetFolder(), Key: at.GetKey(), Value: req.GetValue()})
	registrations[key] = registration
	if err := saveFakeRegistrations(registrations); err != nil {
		return nil, err
	}
	if req.GetValue() == "" {
		return &envvarsv1.PutEnvSourceValueResponse{}, nil
	}
	if err := store.Write(req.GetTier(), at, FakeCellData{Value: req.GetValue(), EnvSource: registration.ID}); err != nil {
		return nil, err
	}
	return &envvarsv1.PutEnvSourceValueResponse{Metadata: store.metadata(req.GetTier(), at)}, nil
}

func refuseFakeSourceOwned(tier environmentv1.Tier, at *envvarsv1.Coordinate) error {
	if at.GetEnvironment() != "" {
		return nil
	}
	registrations, err := LoadFakeRegistrations()
	if err != nil {
		return err
	}
	registration, held := registrations[registrationKey(tier, at.GetSlug())]
	if !held || (at.GetFolder() == "" && slices.Contains(registration.Credentials, at.GetKey())) {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s is read from %s, which owns every value it sets for all of the tier: change it there", at.GetKey(), registration.ID))
}
