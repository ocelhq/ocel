package clitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envsourcewire"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const FakeEnvSourceEnvVar = "OCEL_TEST_FAKE_ENV_SOURCE"

type FakeEnvSource struct {
	Values        []FakeEnvSourceValue `json:"values"`
	URLs          map[string]string    `json:"urls"`
	ReadError     string               `json:"readError"`
	LastAttemptAt int64                `json:"lastAttemptAt"`
	LastSuccessAt int64                `json:"lastSuccessAt"`
	LastError     string               `json:"lastError"`
}

type FakeEnvSourceValue struct {
	Folder string `json:"folder"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

type FakeRegistration struct {
	Descriptor    envsource.Descriptor `json:"descriptor"`
	Folders       []string             `json:"folders"`
	URLs          map[string]string    `json:"urls"`
	LastAttemptAt int64                `json:"lastAttemptAt"`
	LastSuccessAt int64                `json:"lastSuccessAt"`
	LastError     string               `json:"lastError"`
	Created       []FakeEnvSourceValue `json:"created"`
	Updated       []FakeEnvSourceValue `json:"updated"`
}

type FakeRegistrations map[string]FakeRegistration

func fakeRegistrationsPath() (string, error) {
	store := os.Getenv(FakeVarsStoreEnvVar)
	if store == "" {
		return "", errors.New("the fake provider was handed no vars store to keep env source registrations beside")
	}
	return store + ".envsources", nil
}

func LoadFakeRegistrations() (FakeRegistrations, error) {
	registrations := FakeRegistrations{}
	path, err := fakeRegistrationsPath()
	if err != nil {
		return registrations, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return registrations, nil
	}
	if err != nil {
		return nil, err
	}
	return registrations, json.Unmarshal(raw, &registrations)
}

func SaveFakeRegistrations(registrations FakeRegistrations) error {
	path, err := fakeRegistrationsPath()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(registrations)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func FakeRegistrationKey(tier environmentv1.Tier, slug string) string {
	return tier.String() + " " + slug
}

func readFakeEnvSource() (FakeEnvSource, error) {
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
	key := FakeRegistrationKey(req.GetTier(), req.GetSlug())
	registration, registered := registrations[key]

	var values []FakeEnvSourceValue
	if req.GetRegistered() != nil {
		if !registered {
			return &envvarsv1.SyncEnvSourceResponse{Status: builtinStatus()}, nil
		}
		if registration.Descriptor.Kind == envsource.Exec {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"%s in %s reads from exec, whose command runs where ocel deploys: deploy again to read it", req.GetSlug(), fakeClass(req.GetTier())))
		}
	} else {
		descriptor, read := envsourcewire.Decode(req.GetEnvSource())
		if descriptor.Kind == envsource.Builtin {
			if registered {
				if err := clearFakeProvenance(req.GetTier(), req.GetSlug()); err != nil {
					return nil, err
				}
				delete(registrations, key)
				if err := SaveFakeRegistrations(registrations); err != nil {
					return nil, err
				}
			}
			return &envvarsv1.SyncEnvSourceResponse{Status: builtinStatus()}, nil
		}
		folders := req.GetFolders()
		if !slices.Contains(folders, "") {
			folders = append(slices.Clone(folders), "")
		}
		registration = FakeRegistration{Descriptor: descriptor, Folders: folders, Created: registration.Created}
		for at, value := range read {
			values = append(values, FakeEnvSourceValue{Folder: at.Folder, Key: at.Key, Value: string(value.Plaintext)})
		}
	}

	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}
	if err := refuseFakeCredentialUnset(store, req.GetTier(), req.GetSlug(), registration.Descriptor); err != nil {
		return nil, err
	}
	if registration.Descriptor.Kind != envsource.Exec {
		source, err := readFakeEnvSource()
		if err != nil {
			return nil, err
		}
		if source.ReadError != "" {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s: %s", registration.Descriptor.ID(), source.ReadError))
		}
		values = source.Values
		registration.URLs = source.URLs
		registration.LastAttemptAt, registration.LastSuccessAt, registration.LastError = source.LastAttemptAt, source.LastSuccessAt, source.LastError
	} else {
		registration.LastAttemptAt, registration.LastSuccessAt = 1_700_000_100, 1_700_000_100
	}
	registrations[key] = registration
	if err := SaveFakeRegistrations(registrations); err != nil {
		return nil, err
	}

	resp := &envvarsv1.SyncEnvSourceResponse{Status: registration.status()}
	present := map[FakeCoordinate]bool{}
	for _, value := range values {
		if !slices.Contains(registration.Folders, value.Folder) {
			continue
		}
		at := &envvarsv1.Coordinate{Slug: req.GetSlug(), Folder: value.Folder, Key: value.Key}
		present[CoordinateOf(at)] = true
		resp.Present = append(resp.Present, &envvarsv1.Cell{Folder: value.Folder, Key: value.Key})
		if cell := store[FakeCoordinateID(req.GetTier(), at)]; cell.LiveVersion() > 0 {
			latest := cell.Versions[len(cell.Versions)-1]
			if latest.Value == value.Value && latest.EnvSource == registration.Descriptor.ID() {
				continue
			}
		}
		if err := store.Write(req.GetTier(), at, FakeCellData{Value: value.Value, EnvSource: registration.Descriptor.ID()}); err != nil {
			return nil, err
		}
		resp.Written++
	}
	for _, id := range sortedIDs(store) {
		cell := store[id]
		if cell.Tier != req.GetTier() || cell.Coordinate.Slug != req.GetSlug() || cell.Coordinate.Environment != "" || cell.LiveVersion() == 0 {
			continue
		}
		latest := cell.Versions[len(cell.Versions)-1]
		credential := cell.Coordinate.Folder == "" && slices.Contains(registration.Descriptor.CredentialVariables(), cell.Coordinate.Key)
		if latest.Target != nil || credential || !slices.Contains(registration.Folders, cell.Coordinate.Folder) || present[cell.Coordinate] {
			continue
		}
		cell.Deleted = true
		resp.Removed++
	}
	if resp.Removed > 0 {
		if err := SaveFakeStore(store); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func clearFakeProvenance(tier environmentv1.Tier, slug string) error {
	store, err := LoadFakeStore()
	if err != nil {
		return err
	}
	for _, id := range sortedIDs(store) {
		cell := store[id]
		if cell.Tier != tier || cell.Coordinate.Slug != slug || cell.Coordinate.Environment != "" || cell.LiveVersion() == 0 {
			continue
		}
		latest := cell.Versions[len(cell.Versions)-1]
		if latest.EnvSource == "" || latest.Target != nil {
			continue
		}
		at := &envvarsv1.Coordinate{Slug: slug, Folder: cell.Coordinate.Folder, Key: cell.Coordinate.Key}
		if err := store.Write(tier, at, FakeCellData{Value: latest.Value}); err != nil {
			return err
		}
	}
	return nil
}

func refuseFakeCredentialUnset(store FakeStore, tier environmentv1.Tier, slug string, descriptor envsource.Descriptor) error {
	class := edge.Class(fakeClass(tier))
	var messages []string
	var refused []*envvarsv1.CredentialRefusal
	for _, name := range descriptor.CredentialVariables() {
		if store[FakeCoordinateID(tier, &envvarsv1.Coordinate{Slug: slug, Key: name})].LiveVersion() > 0 {
			continue
		}
		reason := fmt.Sprintf("has no value in %s: set it with `%s`", class, envsource.SetCommand(class, name))
		messages = append(messages, fmt.Sprintf("%s logs in with %s, which %s", descriptor.ID(), name, reason))
		refused = append(refused, &envvarsv1.CredentialRefusal{Variable: name, Unset: true, Reason: reason})
	}
	if len(refused) == 0 {
		return nil
	}
	wire := connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.Join(messages, "\n")))
	for _, credential := range refused {
		detail, err := connect.NewErrorDetail(credential)
		if err != nil {
			return err
		}
		wire.AddDetail(detail)
	}
	return wire
}

func fakeClass(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}

func builtinStatus() *envvarsv1.EnvSourceStatus {
	return &envvarsv1.EnvSourceStatus{EnvSource: string(envsource.Builtin)}
}

func (r FakeRegistration) status() *envvarsv1.EnvSourceStatus {
	status := &envvarsv1.EnvSourceStatus{
		EnvSource:     r.Descriptor.ID(),
		Scheduled:     r.Descriptor.IsScheduled(),
		CanCreate:     r.Descriptor.CanCreate(),
		CanUpdate:     r.Descriptor.CanUpdate(),
		LastAttemptAt: r.LastAttemptAt,
		LastSuccessAt: r.LastSuccessAt,
		LastError:     r.LastError,
	}
	status.Credentials = r.Descriptor.CredentialVariables()
	for _, folder := range slices.Sorted(maps.Keys(r.URLs)) {
		status.Links = append(status.Links, &envvarsv1.FolderLink{Folder: folder, Url: r.URLs[folder]})
	}
	return status
}

func (s *deployFakeProviderServer) DescribeEnvSource(_ context.Context, req *envvarsv1.DescribeEnvSourceRequest) (*envvarsv1.DescribeEnvSourceResponse, error) {
	registrations, err := LoadFakeRegistrations()
	if err != nil {
		return nil, err
	}
	registration, registered := registrations[FakeRegistrationKey(req.GetTier(), req.GetSlug())]
	if !registered {
		return &envvarsv1.DescribeEnvSourceResponse{Status: builtinStatus()}, nil
	}
	return &envvarsv1.DescribeEnvSourceResponse{Status: registration.status()}, nil
}

func (s *deployFakeProviderServer) SetEnvSourceValue(_ context.Context, req *envvarsv1.SetEnvSourceValueRequest) (*envvarsv1.SetEnvSourceValueResponse, error) {
	at := req.GetCoordinate()
	if at.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a value for preview environment %q is stored by ocel, never by the env source", at.GetEnvironment()))
	}
	registrations, err := LoadFakeRegistrations()
	if err != nil {
		return nil, err
	}
	key := FakeRegistrationKey(req.GetTier(), at.GetSlug())
	registration, registered := registrations[key]
	if !registered || !registration.Descriptor.CanCreate() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s reads from an env source ocel may not write into: set write to \"values\" or \"missing\" on the tier's env source", at.GetKey()))
	}
	store, err := LoadFakeStore()
	if err != nil {
		return nil, err
	}
	written := FakeEnvSourceValue{Folder: at.GetFolder(), Key: at.GetKey(), Value: req.GetValue()}
	exists := store[FakeCoordinateID(req.GetTier(), at)].LiveVersion() > 0 || slices.ContainsFunc(registration.Created, func(created FakeEnvSourceValue) bool {
		return created.Folder == at.GetFolder() && created.Key == at.GetKey()
	})
	switch {
	case exists && !registration.Descriptor.CanUpdate():
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
			"%s already has %s, and write \"missing\" never overwrites a value there", registration.Descriptor.ID(), at.GetKey()))
	case exists:
		registration.Updated = append(registration.Updated, written)
	default:
		registration.Created = append(registration.Created, written)
	}
	registrations[key] = registration
	if err := SaveFakeRegistrations(registrations); err != nil {
		return nil, err
	}
	if req.GetValue() == "" {
		return &envvarsv1.SetEnvSourceValueResponse{Created: !exists}, nil
	}
	if err := store.Write(req.GetTier(), at, FakeCellData{Value: req.GetValue(), EnvSource: registration.Descriptor.ID()}); err != nil {
		return nil, err
	}
	return &envvarsv1.SetEnvSourceValueResponse{Metadata: store.metadata(req.GetTier(), at), Created: !exists}, nil
}

func refuseFakeEnvSourceOwned(store FakeStore, tier environmentv1.Tier, at *envvarsv1.Coordinate, removing bool) error {
	if at.GetEnvironment() != "" {
		return nil
	}
	registrations, err := LoadFakeRegistrations()
	if err != nil {
		return err
	}
	registration, registered := registrations[FakeRegistrationKey(tier, at.GetSlug())]
	if !registered {
		return nil
	}
	if at.GetFolder() == "" && slices.Contains(registration.Descriptor.CredentialVariables(), at.GetKey()) {
		return nil
	}
	if removing {
		cell := store[FakeCoordinateID(tier, at)]
		if cell.LiveVersion() == 0 || cell.Versions[len(cell.Versions)-1].EnvSource != registration.Descriptor.ID() {
			return nil
		}
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s is read from %s, which owns every value %s sets for all of %s: change it there",
		at.GetKey(), registration.Descriptor.ID(), at.GetSlug(), fakeClass(tier)))
}
