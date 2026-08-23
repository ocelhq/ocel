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
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	cached map[storeKey]values.Store
}

type account struct {
	CFN    bootstrap.CFNDescriber
	Dynamo awsports.DynamoAPI
	KMS    awsports.CryptoAPI
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

func (s *stores) scoped(ctx context.Context, region string, tier environmentv1.Tier, slug string) (values.Store, values.Scope, error) {
	key := storeKey{region: region, preview: tier == environmentv1.Tier_TIER_PREVIEW}
	store, err := s.store(ctx, key)
	if err != nil {
		return values.Store{}, values.Scope{}, err
	}
	if err := values.ValidateProject(slug); err != nil {
		return values.Store{}, values.Scope{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return store, values.Scope{Project: slug, Class: valueClass(key.preview)}, nil
}

func valueClass(preview bool) edge.Class {
	if preview {
		return edge.ClassPreview
	}
	return edge.ClassProduction
}

func (s *stores) store(ctx context.Context, key storeKey) (values.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if store, ok := s.cached[key]; ok {
		return store, nil
	}
	store, err := s.open(ctx, key)
	if err != nil {
		return values.Store{}, err
	}
	if s.cached == nil {
		s.cached = map[storeKey]values.Store{}
	}
	s.cached[key] = store
	return store, nil
}

func (s *stores) open(ctx context.Context, key storeKey) (values.Store, error) {
	reach := s.openAccount
	if reach == nil {
		reach = awsAccount
	}
	cloud, err := reach(ctx, key.region)
	if err != nil {
		return values.Store{}, err
	}

	bootstrapCmd := bootstrapCommand(key.preview)
	deployed, err := checkBootstrap(ctx, cloud.CFN, key.preview)
	if err != nil {
		return values.Store{}, err
	}
	if err := providerkit.CheckSchema(deployed.Schema, deployed.Present, valueClass(key.preview)); err != nil {
		return values.Store{}, providerkit.RefusalError(err)
	}
	if deployed.StateTable == "" || deployed.VarsKeyARN == "" {
		return values.Store{}, fmt.Errorf("account bootstrap is present but its variable store is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	return valueStore(cloud.Dynamo, cloud.KMS, deployed), nil
}

func valueStore(ddb awsports.DynamoAPI, crypto awsports.CryptoAPI, deployed bootstrap.Deployed) values.Store {
	return values.Store{
		Records: awsports.Records{Dynamo: ddb, Table: deployed.StateTable},
		Sealer:  awsports.Sealer{KMS: crypto, KeyARN: deployed.VarsKeyARN},
	}
}

func bootstrapValues(awscfg aws.Config, deployed bootstrap.Deployed) (values.Store, bool) {
	if deployed.StateTable == "" || deployed.VarsKeyARN == "" {
		return values.Store{}, false
	}
	return valueStore(dynamodb.NewFromConfig(awscfg), kms.NewFromConfig(awscfg), deployed), true
}

func linkStore(awscfg aws.Config, deployed bootstrap.Deployed, class string) deploy.LinkStore {
	store, ok := bootstrapValues(awscfg, deployed)
	if !ok {
		return nil
	}
	return publishedLinks{store: store, class: edge.Class(class)}
}

func teardownValues(awscfg aws.Config, deployed bootstrap.Deployed, class string) deploy.ValueStore {
	store, ok := bootstrapValues(awscfg, deployed)
	if !ok {
		return nil
	}
	return projectValues{store: store, class: edge.Class(class)}
}

type projectValues struct {
	store values.Store
	class edge.Class
}

func (p projectValues) Purge(ctx context.Context, slug string) (int, error) {
	return p.store.Purge(ctx, values.Scope{Project: slug, Class: p.class})
}

func referenceOwners(ctx context.Context, awscfg aws.Config, deployed bootstrap.Deployed, class, slug string) (map[values.Coordinate]string, error) {
	store, ok := bootstrapValues(awscfg, deployed)
	if !ok {
		return nil, nil
	}
	return store.ReferenceOwners(ctx, values.Scope{Project: slug, Class: edge.Class(class)})
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
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	metadata, err := store.Set(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetValue(), req.ExpectedVersion)
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.SetValueResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (s *VarsServer) SetReference(ctx context.Context, req *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	if err := s.addressable(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	target := req.GetTarget()
	metadata, err := store.SetReference(ctx, scope, coordinateOf(req.GetCoordinate()), values.Target{
		Project: target.GetSlug(),
		Cell:    values.Cell{Folder: target.GetFolder(), Key: target.GetKey()},
	})
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.SetReferenceResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (s *VarsServer) ListReferences(ctx context.Context, req *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.References(ctx, scope, coordinateOf(req.GetCoordinate()))
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envvarsv1.ListReferencesResponse{References: make([]*envvarsv1.Coordinate, 0, len(found))}
	for _, r := range found {
		resp.References = append(resp.References, coordinateProto(r.Project, r.Coordinate))
	}
	return resp, nil
}

func (s *VarsServer) ListValues(ctx context.Context, req *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.List(ctx, scope)
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envvarsv1.ListValuesResponse{Values: make([]*envvarsv1.ValueMetadata, 0, len(found))}
	for _, m := range found {
		resp.Values = append(resp.Values, metadataProto(scope, m))
	}
	return resp, nil
}

func (s *VarsServer) GetValue(ctx context.Context, req *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	value, err := store.Get(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetReveal())
	if errors.Is(err, values.ErrNotFound) {
		return &envvarsv1.GetValueResponse{}, nil
	}
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.GetValueResponse{
		Found:    true,
		Metadata: metadataProto(scope, value.Metadata),
		Value:    value.Plaintext,
	}, nil
}

func (s *VarsServer) RevealValues(ctx context.Context, req *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	cells := make([]values.Coordinate, 0, len(req.GetCells()))
	for _, c := range req.GetCells() {
		cells = append(cells, coordinateOf(c))
	}
	found, err := store.Reveal(ctx, scope, cells)
	if err != nil {
		return nil, varsError(err)
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

func (s *VarsServer) DeleteValue(ctx context.Context, req *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, scope, coordinateOf(req.GetCoordinate()), req.ExpectedVersion)
	if err != nil {
		return nil, varsError(err)
	}
	return &envvarsv1.DeleteValueResponse{Deleted: deleted}, nil
}

func (s *VarsServer) ListVersions(ctx context.Context, req *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	history, err := store.Versions(ctx, scope, coordinateOf(req.GetCoordinate()))
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

func varsError(err error) error {
	switch {
	case errors.Is(err, values.ErrStaleVersion):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, values.ErrWouldDeepen), errors.Is(err, values.ErrIsReference), errors.Is(err, values.ErrTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, values.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return err
	}
}
