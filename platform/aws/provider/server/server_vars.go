package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
)

type VarsServer struct {
	*stores

	config *sessionConfig

	listEnvironments func(ctx context.Context, region, slug string) ([]string, error)
}

type stores struct {
	openAccount func(ctx context.Context, region string) (account, error)
	openDomain  func(ctx context.Context, region string) (domainClients, error)

	mu     sync.Mutex
	cached map[storeKey]*vars.Store
}

type account struct {
	CFN    bootstrap.CFNDescriber
	Dynamo vars.DynamoAPI
	KMS    vars.CryptoAPI
}

type storeKey struct {
	region  string
	preview bool
}

func awsAccount(ctx context.Context, region string) (account, error) {
	awscfg, err := loadAWS(ctx, region)
	if err != nil {
		return account{}, err
	}
	return account{
		CFN:    cloudformation.NewFromConfig(awscfg),
		Dynamo: dynamodb.NewFromConfig(awscfg),
		KMS:    kms.NewFromConfig(awscfg),
	}, nil
}

func (s *stores) store(ctx context.Context, region string, tier environmentv1.Tier) (*vars.Store, error) {
	key := storeKey{region: region, preview: tier == environmentv1.Tier_TIER_PREVIEW}

	s.mu.Lock()
	defer s.mu.Unlock()
	if store, ok := s.cached[key]; ok {
		return store, nil
	}
	store, err := s.open(ctx, key)
	if err != nil {
		return nil, err
	}
	if s.cached == nil {
		s.cached = map[storeKey]*vars.Store{}
	}
	s.cached[key] = store
	return store, nil
}

func (s *stores) open(ctx context.Context, key storeKey) (*vars.Store, error) {
	reach := s.openAccount
	if reach == nil {
		reach = awsAccount
	}
	cloud, err := reach(ctx, key.region)
	if err != nil {
		return nil, err
	}

	bootstrapCmd := bootstrapCommand(key.preview)
	deployed, err := checkBootstrap(ctx, cloud.CFN, key.preview)
	if err != nil {
		return nil, err
	}
	compat := bootstrap.CheckCompat(deployed.Schema, deployed.Present, bootstrap.RequiredSchema)
	if err := compat.Explain(deployed.Schema, bootstrap.RequiredSchema, bootstrapCmd); err != nil {
		return nil, err
	}
	if deployed.VarsTable == "" || deployed.VarsKeyARN == "" {
		return nil, fmt.Errorf("account bootstrap is present but its variable store is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}

	class := bootstrap.ClassProduction
	if key.preview {
		class = bootstrap.ClassPreview
	}
	return &vars.Store{
		Dynamo: cloud.Dynamo,
		KMS:    cloud.KMS,
		Table:  deployed.VarsTable,
		KeyARN: deployed.VarsKeyARN,
		Class:  class,
	}, nil
}

func linkStore(awscfg aws.Config, deployed bootstrap.Deployed, class string) deploy.LinkStore {
	store := bootstrapStore(awscfg, deployed, class)
	if store == nil {
		return nil
	}
	return store
}

func teardownValues(awscfg aws.Config, deployed bootstrap.Deployed, class string) deploy.ValueStore {
	store := bootstrapStore(awscfg, deployed, class)
	if store == nil {
		return nil
	}
	return store
}

func bootstrapStore(awscfg aws.Config, deployed bootstrap.Deployed, class string) *vars.Store {
	if deployed.VarsTable == "" || deployed.VarsKeyARN == "" {
		return nil
	}
	return &vars.Store{
		Dynamo: dynamodb.NewFromConfig(awscfg),
		KMS:    kms.NewFromConfig(awscfg),
		Table:  deployed.VarsTable,
		KeyARN: deployed.VarsKeyARN,
		Class:  class,
	}
}

func referenceOwners(ctx context.Context, awscfg aws.Config, deployed bootstrap.Deployed, class, slug string) (map[vars.Coordinate]string, error) {
	store := bootstrapStore(awscfg, deployed, class)
	if store == nil {
		return nil, nil
	}
	return store.ReferenceOwners(ctx, slug)
}

func (s *VarsServer) addressable(ctx context.Context, region string, tier environmentv1.Tier, at *envvarsv1.Coordinate) error {
	environment := at.GetEnvironment()
	if environment == "" {
		return nil
	}
	if tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"production has a single environment, so %q addresses no value a production function could read", environment))
	}

	names, err := s.namedEnvironments(ctx, region, at.GetSlug())
	if err != nil {
		return err
	}
	if slices.Contains(names, environment) {
		return nil
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

func (s *VarsServer) namedEnvironments(ctx context.Context, region, slug string) ([]string, error) {
	list := s.listEnvironments
	if list == nil {
		list = previewEnvironments
	}
	return list(ctx, region, slug)
}

func previewEnvironments(ctx context.Context, region, slug string) ([]string, error) {
	resp, err := (&Server{}).listEnvironments(ctx, region, slug)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.GetEnvironments()))
	for _, environment := range resp.GetEnvironments() {
		names = append(names, environment.GetIdentity())
	}
	return names, nil
}

func (s *VarsServer) SetValue(ctx context.Context, req *envvarsv1.SetValueRequest) (*envvarsv1.SetValueResponse, error) {
	if err := s.addressable(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	metadata, err := store.Set(ctx, toCoordinate(req.GetCoordinate()), req.GetValue(), req.ExpectedVersion)
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.SetValueResponse{Metadata: toMetadataProto(metadata)}, nil
}

func (s *VarsServer) SetReference(ctx context.Context, req *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	if err := s.addressable(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	metadata, err := store.SetReference(ctx, toCoordinate(req.GetCoordinate()), toCoordinate(req.GetTarget()), nil)
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.SetReferenceResponse{Metadata: toMetadataProto(metadata)}, nil
}

func (s *VarsServer) ListReferences(ctx context.Context, req *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	found, err := store.References(ctx, toCoordinate(req.GetCoordinate()))
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envvarsv1.ListReferencesResponse{References: make([]*envvarsv1.Coordinate, 0, len(found))}
	for _, c := range found {
		resp.References = append(resp.References, toCoordinateProto(c))
	}
	return resp, nil
}

func (s *VarsServer) ListValues(ctx context.Context, req *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	found, err := store.List(ctx, req.GetSlug())
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envvarsv1.ListValuesResponse{Values: make([]*envvarsv1.ValueMetadata, 0, len(found))}
	for _, m := range found {
		resp.Values = append(resp.Values, toMetadataProto(m))
	}
	return resp, nil
}

func (s *VarsServer) GetValue(ctx context.Context, req *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	value, err := store.Get(ctx, toCoordinate(req.GetCoordinate()), req.GetReveal())
	if errors.Is(err, vars.ErrNotFound) {
		return &envvarsv1.GetValueResponse{}, nil
	}
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.GetValueResponse{
		Found:    true,
		Metadata: toMetadataProto(value.Metadata),
		Value:    value.Plaintext,
	}, nil
}

func (s *VarsServer) RevealValues(ctx context.Context, req *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	cells := make([]vars.Coordinate, 0, len(req.GetCells()))
	for _, c := range req.GetCells() {
		cells = append(cells, vars.Coordinate{
			Slug:        req.GetSlug(),
			Folder:      c.GetFolder(),
			Key:         c.GetKey(),
			Environment: c.GetEnvironment(),
		})
	}
	values, err := store.Reveal(ctx, req.GetSlug(), cells)
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envvarsv1.RevealValuesResponse{Values: make([]*envvarsv1.RevealedValue, 0, len(values))}
	for _, v := range values {
		resp.Values = append(resp.Values, &envvarsv1.RevealedValue{
			Metadata: toMetadataProto(v.Metadata),
			Value:    v.Plaintext,
		})
	}
	return resp, nil
}

func (s *VarsServer) DeleteValue(ctx context.Context, req *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, toCoordinate(req.GetCoordinate()), req.ExpectedVersion)
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.DeleteValueResponse{Deleted: deleted}, nil
}

func (s *VarsServer) ListVersions(ctx context.Context, req *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}
	history, err := store.Versions(ctx, toCoordinate(req.GetCoordinate()))
	if err != nil {
		return nil, varsError(err)
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

func toCoordinate(c *envvarsv1.Coordinate) vars.Coordinate {
	return vars.Coordinate{
		Slug:        c.GetSlug(),
		Folder:      c.GetFolder(),
		Key:         c.GetKey(),
		Environment: c.GetEnvironment(),
	}
}

func toCoordinateProto(c vars.Coordinate) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{
		Slug:        c.Slug,
		Folder:      c.Folder,
		Key:         c.Key,
		Environment: c.Environment,
	}
}

func toMetadataProto(m vars.Metadata) *envvarsv1.ValueMetadata {
	out := &envvarsv1.ValueMetadata{
		Coordinate: toCoordinateProto(m.Coordinate),
		Version:    m.Version,
		UpdatedAt:  m.UpdatedAt,
		Size:       m.Size,
	}
	if m.Target.Slug != "" {
		out.Target = toCoordinateProto(m.Target)
	}
	return out
}

func varsError(err error) error {
	switch {
	case errors.Is(err, vars.ErrStaleVersion):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, vars.ErrWouldDeepen), errors.Is(err, vars.ErrIsReference):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, vars.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return err
	}
}
