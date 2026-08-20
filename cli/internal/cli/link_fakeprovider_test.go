package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const linkFakeStoreEnvVar = "OCEL_TEST_FAKE_LINKS_STORE"

const fakeLinkOwnerOcel = "OCEL"

type fakeLink struct {
	Tier        environmentv1.Tier     `json:"tier"`
	Slug        string                 `json:"slug"`
	Environment string                 `json:"environment"`
	Name        string                 `json:"name"`
	Type        linksv1.LinkType       `json:"type"`
	Source      string                 `json:"source"`
	Owner       string                 `json:"owner"`
	Version     uint64                 `json:"version"`
	Properties  []naming.PropertyShape `json:"properties"`
}

type fakeLinkStore map[string]*fakeLink

func fakeLinkID(tier environmentv1.Tier, slug, environment, name string) string {
	return fmt.Sprintf("%s %q %q %q", tier, slug, environment, name)
}

func loadFakeLinkStore() (fakeLinkStore, error) {
	store := fakeLinkStore{}
	raw, err := os.ReadFile(os.Getenv(linkFakeStoreEnvVar))
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	return store, json.Unmarshal(raw, &store)
}

func saveFakeLinkStore(store fakeLinkStore) error {
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(os.Getenv(linkFakeStoreEnvVar), raw, 0o600)
}

func (s *deployFakeProviderServer) SetLink(ctx context.Context, req *envvarsv1.SetLinkRequest) (*envvarsv1.SetLinkResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	link := req.GetLink()
	if link.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(
			"the link carries no name; the name is what a consuming app binds to"))
	}
	if naming.LinkTypeOf(link) == linksv1.LinkType_LINK_TYPE_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"link %s carries no properties, so it has no type a consumer can resolve it against", link.GetName()))
	}
	if req.GetOwner() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(
			"a publisher name is required: it is what keeps one publisher from taking another's records"))
	}
	if req.GetOwner() == fakeLinkOwnerOcel {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"publisher name %q names ocel's own provisioning; every record it stamps would be one ocel's next deploy may prune",
			fakeLinkOwnerOcel))
	}

	store, err := loadFakeLinkStore()
	if err != nil {
		return nil, err
	}
	id := fakeLinkID(req.GetTier(), req.GetSlug(), req.GetEnvironment(), link.GetName())
	held := store[id]
	if held != nil && held.Owner != req.GetOwner() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"link %s is already published by %s, so %s may not take it: one link name belongs to one publisher",
			link.GetName(), held.Owner, req.GetOwner()))
	}

	version := uint64(1)
	if held != nil {
		version = held.Version + 1
	}
	store[id] = &fakeLink{
		Tier:        req.GetTier(),
		Slug:        req.GetSlug(),
		Environment: req.GetEnvironment(),
		Name:        link.GetName(),
		Type:        naming.LinkTypeOf(link),
		Source:      link.GetSource(),
		Owner:       req.GetOwner(),
		Version:     version,
		Properties:  naming.LinkPropertyShapes(link),
	}
	if err := saveFakeLinkStore(store); err != nil {
		return nil, err
	}
	return &envvarsv1.SetLinkResponse{Version: version}, nil
}

func (s *deployFakeProviderServer) RemoveLink(ctx context.Context, req *envvarsv1.RemoveLinkRequest) (*envvarsv1.RemoveLinkResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeLinkStore()
	if err != nil {
		return nil, err
	}
	id := fakeLinkID(req.GetTier(), req.GetSlug(), req.GetEnvironment(), req.GetName())
	if store[id] == nil {
		return &envvarsv1.RemoveLinkResponse{}, nil
	}
	delete(store, id)
	if err := saveFakeLinkStore(store); err != nil {
		return nil, err
	}
	return &envvarsv1.RemoveLinkResponse{Removed: true}, nil
}

func (s *deployFakeProviderServer) ListLinks(ctx context.Context, req *envvarsv1.ListLinksRequest) (*envvarsv1.ListLinksResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	store, err := loadFakeLinkStore()
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(store))
	for id := range store {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	resp := &envvarsv1.ListLinksResponse{}
	for _, id := range ids {
		held := store[id]
		if held.Tier != req.GetTier() || held.Slug != req.GetSlug() {
			continue
		}
		if held.Environment != req.GetEnvironment() && held.Environment != "" {
			continue
		}
		resp.Links = append(resp.Links, &envvarsv1.LinkSummary{
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
