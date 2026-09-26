package envvarsserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Backend struct {
	Records      records.Store
	Cipher       records.Cipher
	VerifyGrants func(ctx context.Context, binding provider.Binding) error
}

type BackendSource interface {
	Read() (Backend, error)
}

type Service struct {
	Source BackendSource
}

type FixedBackend Backend

func (s FixedBackend) Read() (Backend, error) { return Backend(s), nil }

func (h *Service) values(tier environmentv1.Tier) (envvars.Store, edge.Class, error) {
	backend, err := h.Source.Read()
	if err != nil {
		return envvars.Store{}, "", err
	}
	class := edge.ClassProduction
	if tier == environmentv1.Tier_TIER_PREVIEW {
		class = edge.ClassPreview
	}
	return envvars.Store{Records: backend.Records, Cipher: backend.Cipher}, class, nil
}

func (h *Service) scoped(tier environmentv1.Tier, slug string) (envvars.Store, envvars.Scope, error) {
	store, class, err := h.values(tier)
	if err != nil {
		return envvars.Store{}, envvars.Scope{}, err
	}
	if err := envvars.ValidateProject(slug); err != nil {
		return envvars.Store{}, envvars.Scope{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return store, envvars.Scope{Project: slug, Class: class}, nil
}

func (h *Service) addressable(ctx context.Context, tier environmentv1.Tier, at *envvarsv1.Coordinate) error {
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

func (h *Service) namedEnvironments(ctx context.Context, slug string) ([]string, error) {
	backend, err := h.Source.Read()
	if err != nil {
		return nil, err
	}
	stacks, err := stackrecords.StackNames(ctx, backend.Records, edge.ClassPreview, slug)
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

func coordinateOf(c *envvarsv1.Coordinate) envvars.Coordinate {
	return envvars.Coordinate{
		Cell:        envvars.Cell{Folder: c.GetFolder(), Key: c.GetKey()},
		Environment: c.GetEnvironment(),
	}
}

func coordinateProto(slug string, c envvars.Coordinate) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{
		Slug:        slug,
		Folder:      c.Folder,
		Key:         c.Key,
		Environment: c.Environment,
	}
}

func metadataProto(scope envvars.Scope, m envvars.Metadata) *envvarsv1.ValueMetadata {
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
	case errors.Is(err, envvars.ErrStaleVersion):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, envvars.ErrDangling):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, envvars.ErrWouldDeepen), errors.Is(err, envvars.ErrIsReference), errors.Is(err, envvars.ErrTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, envvars.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return provider.RefusalError(err)
	}
}
