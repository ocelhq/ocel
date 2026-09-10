package clitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const FakeBindingsStoreEnvVar = "OCEL_TEST_FAKE_BINDINGS_STORE"

const fakeBindingOwnerOcel = "OCEL"

type fakeBinding struct {
	Tier        environmentv1.Tier     `json:"tier"`
	Slug        string                 `json:"slug"`
	Environment string                 `json:"environment"`
	Name        string                 `json:"name"`
	Type        bindingsv1.BindingType `json:"type"`
	Source      string                 `json:"source"`
	Owner       string                 `json:"owner"`
	Version     uint64                 `json:"version"`
	Properties  []naming.PropertyShape `json:"properties"`
}

type fakeBindingStore map[string]*fakeBinding

func fakeBindingID(tier environmentv1.Tier, slug, environment, name string) string {
	return fmt.Sprintf("%s %q %q %q", tier, slug, environment, name)
}

func loadFakeBindingStore() (fakeBindingStore, error) {
	store := fakeBindingStore{}
	raw, err := os.ReadFile(os.Getenv(FakeBindingsStoreEnvVar))
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	return store, json.Unmarshal(raw, &store)
}

func saveFakeBindingStore(store fakeBindingStore) error {
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(os.Getenv(FakeBindingsStoreEnvVar), raw, 0o600)
}

func (s *deployFakeProviderServer) SetBinding(ctx context.Context, req *envvarsv1.SetBindingRequest) (*envvarsv1.SetBindingResponse, error) {
	binding := req.GetBinding()
	if binding.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(
			"the binding carries no name; the name is what a consuming app binds to"))
	}
	if naming.BindingTypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"binding %s carries no properties, so it has no type a consumer can resolve it against", binding.GetName()))
	}
	if req.GetOwner() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(
			"a publisher name is required: it is what keeps one publisher from taking another's records"))
	}
	if req.GetOwner() == fakeBindingOwnerOcel {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"publisher name %q names ocel's own provisioning; every record it stamps would be one ocel's next deploy may prune",
			fakeBindingOwnerOcel))
	}

	store, err := loadFakeBindingStore()
	if err != nil {
		return nil, err
	}
	id := fakeBindingID(req.GetTier(), req.GetSlug(), req.GetEnvironment(), binding.GetName())
	held := store[id]
	if held != nil && held.Owner != req.GetOwner() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"binding %s is already published by %s, so %s may not take it: one binding name belongs to one publisher",
			binding.GetName(), held.Owner, req.GetOwner()))
	}

	version := uint64(1)
	if held != nil {
		version = held.Version + 1
	}
	store[id] = &fakeBinding{
		Tier:        req.GetTier(),
		Slug:        req.GetSlug(),
		Environment: req.GetEnvironment(),
		Name:        binding.GetName(),
		Type:        naming.BindingTypeOf(binding),
		Source:      binding.GetSource(),
		Owner:       req.GetOwner(),
		Version:     version,
		Properties:  naming.BindingPropertyShapes(binding),
	}
	if err := saveFakeBindingStore(store); err != nil {
		return nil, err
	}
	return &envvarsv1.SetBindingResponse{Version: version}, nil
}

func (s *deployFakeProviderServer) RemoveBinding(ctx context.Context, req *envvarsv1.RemoveBindingRequest) (*envvarsv1.RemoveBindingResponse, error) {
	store, err := loadFakeBindingStore()
	if err != nil {
		return nil, err
	}
	id := fakeBindingID(req.GetTier(), req.GetSlug(), req.GetEnvironment(), req.GetName())
	if store[id] == nil {
		return &envvarsv1.RemoveBindingResponse{}, nil
	}
	delete(store, id)
	if err := saveFakeBindingStore(store); err != nil {
		return nil, err
	}
	return &envvarsv1.RemoveBindingResponse{Removed: true}, nil
}

func (s *deployFakeProviderServer) ListBindings(ctx context.Context, req *envvarsv1.ListBindingsRequest) (*envvarsv1.ListBindingsResponse, error) {
	store, err := loadFakeBindingStore()
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(store))
	for id := range store {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	resp := &envvarsv1.ListBindingsResponse{}
	for _, id := range ids {
		held := store[id]
		if held.Tier != req.GetTier() || held.Slug != req.GetSlug() {
			continue
		}
		if held.Environment != req.GetEnvironment() && held.Environment != "" {
			continue
		}
		resp.Bindings = append(resp.Bindings, &envvarsv1.BindingSummary{
			Name:       held.Name,
			Type:       held.Type,
			Source:     held.Source,
			Owner:      held.Owner,
			Version:    held.Version,
			Properties: naming.PropertyShapeMessages(held.Properties),
		})
	}
	return resp, nil
}
