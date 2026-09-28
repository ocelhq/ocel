package envvarsserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type Backend struct {
	Records       records.Store
	Cipher        records.Cipher
	VerifyGrants  func(ctx context.Context, binding provider.Binding) error
	ProveIdentity func(ctx context.Context, audience string) (envsource.IdentityProof, error)
}

type BackendSource interface {
	Read() (Backend, error)
}

type Service struct {
	Source               BackendSource
	CallerNamesEnvSource bool
}

type FixedBackend Backend

func (s FixedBackend) Read() (Backend, error) { return Backend(s), nil }

func (h *Service) values(requested environmentv1.Tier) (envvars.Store, environment.Tier, error) {
	backend, err := h.Source.Read()
	if err != nil {
		return envvars.Store{}, "", err
	}
	tier, err := decodeTier(requested)
	if err != nil {
		return envvars.Store{}, "", err
	}
	return envvars.Store{Records: backend.Records, Cipher: backend.Cipher}, tier, nil
}

func decodeTier(tier environmentv1.Tier) (environment.Tier, error) {
	switch tier {
	case environmentv1.Tier_TIER_PRODUCTION:
		return environment.TierProduction, nil
	case environmentv1.Tier_TIER_PREVIEW:
		return environment.TierPreview, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"this request names no tier: name the %s or the %s tier", environment.TierProduction, environment.TierPreview))
	}
}

func (h *Service) scoped(requested environmentv1.Tier, slug string) (envvars.Store, envvars.Scope, error) {
	store, tier, err := h.values(requested)
	if err != nil {
		return envvars.Store{}, envvars.Scope{}, err
	}
	if err := envvars.ValidateProject(slug); err != nil {
		return envvars.Store{}, envvars.Scope{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return store, envvars.Scope{Project: slug, Tier: tier}, nil
}

func (h *Service) addressable(ctx context.Context, tier environmentv1.Tier, at *envvarsv1.Coordinate) error {
	env := at.GetEnvironment()
	if env == "" {
		return nil
	}
	if tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"production has a single environment, so %q addresses no value a production function could read", env))
	}
	named, err := h.namedEnvironments(ctx, at.GetSlug())
	if err != nil {
		return err
	}
	if slices.Contains(named, env) {
		return nil
	}
	if len(named) == 0 {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"no preview environment named %q exists, and this project has none at all; deploy one with `ocel preview` before setting a value only it would read", env))
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"no preview environment named %q exists, so nothing would ever read that value. This project's environments are: %s",
		env, strings.Join(named, ", ")))
}

func (h *Service) namedEnvironments(ctx context.Context, slug string) ([]string, error) {
	backend, err := h.Source.Read()
	if err != nil {
		return nil, err
	}
	stacks, err := stackrecords.StackNames(ctx, backend.Records, environment.TierPreview, slug)
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
		EnvSource:  m.Provenance.EnvSource,
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
