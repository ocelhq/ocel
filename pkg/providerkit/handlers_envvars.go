package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func (h *handlers) values(tier environmentv1.Tier) (values.Store, Class, error) {
	provider, err := h.session.use()
	if err != nil {
		return values.Store{}, "", err
	}
	class := ClassProduction
	if tier == environmentv1.Tier_TIER_PREVIEW {
		class = ClassPreview
	}
	return values.Store{Records: provider.Records(), Sealer: provider.Sealer()}, class, nil
}

func (h *handlers) scoped(tier environmentv1.Tier, slug string) (values.Store, values.Scope, error) {
	store, class, err := h.values(tier)
	if err != nil {
		return values.Store{}, values.Scope{}, err
	}
	if err := values.ValidateProject(slug); err != nil {
		return values.Store{}, values.Scope{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return store, values.Scope{Project: slug, Class: class}, nil
}

func (h *handlers) addressable(ctx context.Context, tier environmentv1.Tier, at *envvarsv1.Coordinate) error {
	environment := at.GetEnvironment()
	if environment == "" {
		return nil
	}
	if tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"production has a single environment, so %q addresses no value a production function could read", environment))
	}
	named, err := h.namedEnvironments(ctx, at.GetSlug())
	if err != nil {
		return err
	}
	if slices.Contains(named, environment) {
		return nil
	}
	if len(named) == 0 {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"no preview environment named %q exists, and this project has none at all; deploy one with `ocel preview` before setting a value only it would read", environment))
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"no preview environment named %q exists, so nothing would ever read that value. This project's environments are: %s",
		environment, strings.Join(named, ", ")))
}

func (h *handlers) namedEnvironments(ctx context.Context, slug string) ([]string, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	stacks, err := stackNames(ctx, provider.Records(), slug)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var named []string
	for _, stack := range stacks {
		if stack.Env == "" || slices.Contains(named, stack.Env) {
			continue
		}
		named = append(named, stack.Env)
	}
	slices.Sort(named)
	return named, nil
}

func (h *handlers) SetValue(ctx context.Context, req *envvarsv1.SetValueRequest) (*envvarsv1.SetValueResponse, error) {
	if err := h.addressable(ctx, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	metadata, err := store.Set(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetValue(), req.ExpectedVersion)
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.SetValueResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (h *handlers) ListValues(ctx context.Context, req *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.List(ctx, scope)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.ListValuesResponse{Values: make([]*envvarsv1.ValueMetadata, 0, len(found))}
	for _, m := range found {
		resp.Values = append(resp.Values, metadataProto(scope, m))
	}
	return resp, nil
}

func (h *handlers) GetValue(ctx context.Context, req *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	value, err := store.Get(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetReveal())
	if errors.Is(err, values.ErrNotFound) {
		return &envvarsv1.GetValueResponse{}, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.GetValueResponse{
		Found:    true,
		Metadata: metadataProto(scope, value.Metadata),
		Value:    value.Plaintext,
	}, nil
}

func (h *handlers) RevealValues(ctx context.Context, req *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	cells := make([]values.Coordinate, 0, len(req.GetCells()))
	for _, c := range req.GetCells() {
		cells = append(cells, coordinateOf(c))
	}
	found, err := store.Reveal(ctx, scope, cells)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.RevealValuesResponse{Values: make([]*envvarsv1.RevealedValue, 0, len(found))}
	for _, v := range found {
		resp.Values = append(resp.Values, &envvarsv1.RevealedValue{
			Metadata: metadataProto(scope, v.Metadata),
			Value:    v.Plaintext,
		})
	}
	return resp, nil
}

func (h *handlers) DeleteValue(ctx context.Context, req *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, scope, coordinateOf(req.GetCoordinate()), req.ExpectedVersion)
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.DeleteValueResponse{Deleted: deleted}, nil
}

func (h *handlers) SetReference(ctx context.Context, req *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	if err := h.addressable(ctx, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	target := req.GetTarget()
	if target.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a reference resolves against %s's class-wide value; %q is an environment of the project holding the reference, and names nothing in the target's",
			target.GetKey(), target.GetEnvironment()))
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	metadata, err := store.SetReference(ctx, scope, coordinateOf(req.GetCoordinate()), values.Target{
		Project: target.GetSlug(),
		Cell:    values.Cell{Folder: target.GetFolder(), Key: target.GetKey()},
	})
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.SetReferenceResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (h *handlers) ListReferences(ctx context.Context, req *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.References(ctx, scope, coordinateOf(req.GetCoordinate()))
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.ListReferencesResponse{References: make([]*envvarsv1.Coordinate, 0, len(found))}
	for _, r := range found {
		resp.References = append(resp.References, coordinateProto(r.Project, r.Coordinate))
	}
	return resp, nil
}

func (h *handlers) ListVersions(ctx context.Context, req *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	history, err := store.Versions(ctx, scope, coordinateOf(req.GetCoordinate()))
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.ListVersionsResponse{Versions: make([]*envvarsv1.VersionEntry, 0, len(history))}
	for _, v := range history {
		resp.Versions = append(resp.Versions, &envvarsv1.VersionEntry{
			Version:   v.Version,
			CreatedAt: v.CreatedAt,
			Size:      v.Size,
		})
	}
	return resp, nil
}

func (h *handlers) SetLink(ctx context.Context, req *envvarsv1.SetLinkRequest) (*envvarsv1.SetLinkResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	link := req.GetLink()
	if err := values.ValidateLinkName(req.GetEnvironment(), link.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := ValidatePublisher(req.GetOwner()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := RefuseUnsourced(req.GetOwner(), link); err != nil {
		return nil, linksError(err)
	}
	if err := VerifyLink(link); err != nil {
		return nil, linksError(err)
	}
	pair, err := LinkPair(req.GetOwner(), link)
	if err != nil {
		return nil, linksError(err)
	}

	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	version, err := store.SetLink(ctx, scope, req.GetEnvironment(), req.GetOwner(), link.GetName(), pair)
	if err != nil {
		return nil, linksError(err)
	}
	return &envvarsv1.SetLinkResponse{Version: uint64(version)}, nil
}

func (h *handlers) RemoveLink(ctx context.Context, req *envvarsv1.RemoveLinkRequest) (*envvarsv1.RemoveLinkResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := values.ValidateLinkName(req.GetEnvironment(), req.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	removed, err := store.RemoveLink(ctx, scope, req.GetEnvironment(), req.GetName())
	if err != nil {
		return nil, linksError(err)
	}
	return &envvarsv1.RemoveLinkResponse{Removed: removed}, nil
}

func (h *handlers) ListLinks(ctx context.Context, req *envvarsv1.ListLinksRequest) (*envvarsv1.ListLinksResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.ListLinks(ctx, scope, req.GetEnvironment())
	if err != nil {
		return nil, linksError(err)
	}
	resp := &envvarsv1.ListLinksResponse{Links: make([]*envvarsv1.LinkSummary, 0, len(found))}
	for _, published := range found {
		link, err := DecodeLink(published.Record)
		if err != nil {
			return nil, linksError(fmt.Errorf("read link %s's record: %v: %w", published.Name, err, ErrUnreadableRecord))
		}
		shapes, err := DecodeShapes(published.Shapes)
		if err != nil {
			return nil, linksError(fmt.Errorf("read link %s's shape: %v: %w", published.Name, err, ErrUnreadableRecord))
		}
		resp.Links = append(resp.Links, &envvarsv1.LinkSummary{
			Name:       published.Name,
			Type:       naming.LinkTypeOf(link),
			Source:     link.GetSource(),
			Owner:      published.Owner,
			Version:    uint64(published.Version),
			Properties: naming.PropertyShapeMessages(shapes),
		})
	}
	return resp, nil
}

func linkTarget(tier environmentv1.Tier, environment string) error {
	if environment != "" && tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"environment %q is named alongside class %q: an ocel coordinate is a class and, in %s, one preview environment; leave the environment off",
			environment, ClassProduction, ClassPreview))
	}
	return nil
}

func coordinateOf(c *envvarsv1.Coordinate) values.Coordinate {
	return values.Coordinate{
		Cell:        values.Cell{Folder: c.GetFolder(), Key: c.GetKey()},
		Environment: c.GetEnvironment(),
	}
}

func coordinateProto(slug string, c values.Coordinate) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{
		Slug:        slug,
		Folder:      c.Folder,
		Key:         c.Key,
		Environment: c.Environment,
	}
}

func metadataProto(scope values.Scope, m values.Metadata) *envvarsv1.ValueMetadata {
	out := &envvarsv1.ValueMetadata{
		Coordinate: coordinateProto(scope.Project, m.Coordinate),
		Version:    m.Version,
		UpdatedAt:  m.UpdatedAt,
		Size:       m.Size,
	}
	if m.Target != nil {
		out.Target = &envvarsv1.Coordinate{
			Slug:   m.Target.Project,
			Folder: m.Target.Folder,
			Key:    m.Target.Key,
		}
	}
	return out
}

func valuesError(err error) error {
	switch {
	case errors.Is(err, values.ErrStaleVersion):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, values.ErrWouldDeepen), errors.Is(err, values.ErrIsReference), errors.Is(err, values.ErrTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, values.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return refusalError(err)
	}
}

func linksError(err error) error {
	switch {
	case errors.Is(err, values.ErrClaimed):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, values.ErrTornPair):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, ErrUnsourced), errors.Is(err, ErrUnreadableRecord),
		errors.Is(err, ErrUnscopedGrant), errors.Is(err, ErrUnattachedGrant):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, values.ErrNotPublished):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return valuesError(err)
	}
}
