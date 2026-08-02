package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/aws/vars"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
)

// VarsServer implements envv1connect.EnvVarsServiceHandler. Every call reaches
// the store for the class it names — the table and the key are per substrate,
// so the class is which store is opened rather than a filter over one.
type VarsServer struct {
	// openAccount reaches the cloud a store is opened against. Nil is AWS
	// itself.
	openAccount func(ctx context.Context, region string) (account, error)

	// listEnvironments enumerates the preview environments a project has. Nil is
	// this provider's own DeploymentService, which is the only authority on
	// which names exist. It is a seam so the write guard can be driven without a
	// Pulumi backend behind it.
	listEnvironments func(ctx context.Context, options []byte, slug string) ([]string, error)

	mu     sync.Mutex
	stores map[storeKey]*vars.Store
}

// account is the cloud a variable store is opened against: the
// CloudFormation client its coordinates are read from, and the clients the
// store itself reads and decrypts with.
type account struct {
	CFN    bootstrap.CFNDescriber
	Dynamo vars.DynamoAPI
	KMS    vars.CryptoAPI
}

// storeKey is everything opening a store varies on: the options the account is
// resolved from, and which substrate's bootstrap stack holds its coordinates.
type storeKey struct {
	options options
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

// store returns the substrate's variable store, opening it at most once per
// session. A store's coordinates come from its bootstrap stack, which does not
// change while the provider is running, so opening it per RPC would spend a
// CloudFormation describe on every value a deploy reads. The lock is held
// across the open so a burst of concurrent reads costs one describe rather
// than one each.
func (s *VarsServer) store(ctx context.Context, raw []byte, class deploymentsv1.Environment_Class) (*vars.Store, error) {
	opts, err := parseOptions(raw)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	key := storeKey{options: opts, preview: class == deploymentsv1.Environment_CLASS_PREVIEW}

	s.mu.Lock()
	defer s.mu.Unlock()
	if store, ok := s.stores[key]; ok {
		return store, nil
	}
	store, err := s.open(ctx, key)
	if err != nil {
		return nil, err
	}
	if s.stores == nil {
		s.stores = map[storeKey]*vars.Store{}
	}
	s.stores[key] = store
	return store, nil
}

// open resolves the substrate's variable store from its bootstrap stack,
// applying the same version gate every other RPC applies: an account
// bootstrapped before the store existed gets the re-run remedy rather than an
// obscure failure against a table that is not there.
func (s *VarsServer) open(ctx context.Context, key storeKey) (*vars.Store, error) {
	reach := s.openAccount
	if reach == nil {
		reach = awsAccount
	}
	cloud, err := reach(ctx, key.options.Region)
	if err != nil {
		return nil, err
	}

	bootstrapCmd := bootstrapCommand(key.preview)
	deployed, err := checkBootstrap(ctx, cloud.CFN, key.preview)
	if err != nil {
		return nil, err
	}
	compat := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion)
	if err := compat.Explain(deployed.Version, bootstrap.RequiredBootstrapVersion, bootstrapCmd); err != nil {
		return nil, err
	}
	if deployed.VarsTable == "" || deployed.VarsKeyARN == "" {
		return nil, fmt.Errorf("account bootstrap is present but its variable store is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}

	substrateClass := bootstrap.ClassProduction
	if key.preview {
		substrateClass = bootstrap.ClassPreview
	}
	return &vars.Store{
		Dynamo: cloud.Dynamo,
		KMS:    cloud.KMS,
		Table:  deployed.VarsTable,
		KeyARN: deployed.VarsKeyARN,
		Class:  substrateClass,
	}, nil
}

// teardownValues opens the substrate's variable store for a teardown to empty,
// or reports none when the bootstrap predates the store: an account with no
// vars table holds no values to remove, and failing the whole teardown over
// that would strand everything else the project owns.
func teardownValues(awscfg aws.Config, deployed bootstrap.Deployed, class string) deploy.ValueStore {
	store := substrateStore(awscfg, deployed, class)
	if store == nil {
		return nil
	}
	return store
}

// substrateStore opens the class's variable store from an already-described
// bootstrap, or reports none when that bootstrap predates the store. It is
// separate from teardownValues because the Config field is an interface: a
// typed nil handed to one would read as configured.
func substrateStore(awscfg aws.Config, deployed bootstrap.Deployed, class string) *vars.Store {
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

// referenceOwners maps each of a project's cells that reads another project's
// value to the project owning it, which is what the deploy scopes each app's
// read grant by. An account whose bootstrap predates the store references
// nothing, because it holds nothing.
func referenceOwners(ctx context.Context, awscfg aws.Config, deployed bootstrap.Deployed, class, slug string) (map[vars.Coordinate]string, error) {
	store := substrateStore(awscfg, deployed, class)
	if store == nil {
		return nil, nil
	}
	return store.ReferenceOwners(ctx, slug)
}

// addressable refuses an override written against an address no runtime will
// ever ask for. An override is identified by exactly the key a preview's
// functions derive from their own identity, so a name nothing enumerates names
// a value that is invisible from the moment it lands — and production has a
// single environment, so it has no such name at all.
//
// It lives here because this is the last thing before the store: the CLI and
// the bundled UI both state the rule where they can offer a picker instead of a
// refusal, but neither of them is what the store is protected by.
//
// Only a write is guarded. Reading and removing an override whose environment
// is gone is the whole remedy for one, so neither is ever refused.
func (s *VarsServer) addressable(ctx context.Context, options []byte, class deploymentsv1.Environment_Class, at *envv1.Coordinate) error {
	environment := at.GetEnvironment()
	if environment == "" {
		return nil
	}
	if class != deploymentsv1.Environment_CLASS_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"production has a single environment, so %q addresses no value a production function could read", environment))
	}

	names, err := s.namedEnvironments(ctx, options, at.GetSlug())
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
	sort.Strings(names)
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"no preview environment named %q exists, so nothing would ever read that value. This project's environments are: %s",
		environment, strings.Join(names, ", ")))
}

func (s *VarsServer) namedEnvironments(ctx context.Context, options []byte, slug string) ([]string, error) {
	list := s.listEnvironments
	if list == nil {
		list = previewEnvironments
	}
	return list(ctx, options, slug)
}

// previewEnvironments is the provider's own enumeration, reached through the
// same handler the CLI calls, so the names a write is judged against and the
// names a picker offers can never be two different answers.
func previewEnvironments(ctx context.Context, options []byte, slug string) ([]string, error) {
	resp, err := (&Server{}).ListEnvironments(ctx, &deploymentsv1.ListEnvironmentsRequest{Options: options, Slug: slug})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.GetEnvironments()))
	for _, environment := range resp.GetEnvironments() {
		names = append(names, environment.GetIdentity())
	}
	return names, nil
}

func (s *VarsServer) SetValue(ctx context.Context, req *envv1.SetValueRequest) (*envv1.SetValueResponse, error) {
	if err := s.addressable(ctx, req.GetOptions(), req.GetClass(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}
	metadata, err := store.Set(ctx, toCoordinate(req.GetCoordinate()), req.GetValue(), req.ExpectedVersion)
	if err != nil {
		return nil, varsError(err)
	}
	return &envv1.SetValueResponse{Metadata: toMetadataProto(metadata)}, nil
}

// SetReference points a cell at a value owned elsewhere. It is guarded like
// any other write to that cell — an override is still only addressable at an
// environment the provider enumerates — because what varies here is what the
// cell holds, not where it is.
func (s *VarsServer) SetReference(ctx context.Context, req *envv1.SetReferenceRequest) (*envv1.SetReferenceResponse, error) {
	if err := s.addressable(ctx, req.GetOptions(), req.GetClass(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}
	metadata, err := store.SetReference(ctx, toCoordinate(req.GetCoordinate()), toCoordinate(req.GetTarget()), req.ExpectedVersion)
	if err != nil {
		return nil, varsError(err)
	}
	return &envv1.SetReferenceResponse{Metadata: toMetadataProto(metadata)}, nil
}

func (s *VarsServer) ListReferences(ctx context.Context, req *envv1.ListReferencesRequest) (*envv1.ListReferencesResponse, error) {
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}
	found, err := store.References(ctx, toCoordinate(req.GetCoordinate()))
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envv1.ListReferencesResponse{References: make([]*envv1.Coordinate, 0, len(found))}
	for _, c := range found {
		resp.References = append(resp.References, toCoordinateProto(c))
	}
	return resp, nil
}

func (s *VarsServer) ListValues(ctx context.Context, req *envv1.ListValuesRequest) (*envv1.ListValuesResponse, error) {
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}
	found, err := store.List(ctx, req.GetSlug())
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envv1.ListValuesResponse{Values: make([]*envv1.ValueMetadata, 0, len(found))}
	for _, m := range found {
		resp.Values = append(resp.Values, toMetadataProto(m))
	}
	return resp, nil
}

func (s *VarsServer) GetValue(ctx context.Context, req *envv1.GetValueRequest) (*envv1.GetValueResponse, error) {
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}
	value, err := store.Get(ctx, toCoordinate(req.GetCoordinate()), req.GetReveal())
	if errors.Is(err, vars.ErrNotFound) {
		return &envv1.GetValueResponse{}, nil
	}
	if err != nil {
		return nil, varsError(err)
	}
	return &envv1.GetValueResponse{
		Found:    true,
		Metadata: toMetadataProto(value.Metadata),
		Value:    value.Plaintext,
	}, nil
}

// RevealValues is the batch behind GetValue: one query over the project's
// current values and one decrypt per named cell that holds one, so a caller
// resolving a whole set pays a single round trip to the table rather than one
// per variable.
func (s *VarsServer) RevealValues(ctx context.Context, req *envv1.RevealValuesRequest) (*envv1.RevealValuesResponse, error) {
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
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
	resp := &envv1.RevealValuesResponse{Values: make([]*envv1.RevealedValue, 0, len(values))}
	for _, v := range values {
		resp.Values = append(resp.Values, &envv1.RevealedValue{
			Metadata: toMetadataProto(v.Metadata),
			Value:    v.Plaintext,
		})
	}
	return resp, nil
}

func (s *VarsServer) DeleteValue(ctx context.Context, req *envv1.DeleteValueRequest) (*envv1.DeleteValueResponse, error) {
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, toCoordinate(req.GetCoordinate()), req.ExpectedVersion)
	if err != nil {
		return nil, varsError(err)
	}
	return &envv1.DeleteValueResponse{Deleted: deleted}, nil
}

func (s *VarsServer) ListVersions(ctx context.Context, req *envv1.ListVersionsRequest) (*envv1.ListVersionsResponse, error) {
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}
	history, err := store.Versions(ctx, toCoordinate(req.GetCoordinate()))
	if err != nil {
		return nil, varsError(err)
	}
	resp := &envv1.ListVersionsResponse{Versions: make([]*envv1.VersionEntry, 0, len(history))}
	for _, v := range history {
		resp.Versions = append(resp.Versions, &envv1.VersionEntry{
			Version:   v.Version,
			CreatedAt: v.CreatedAt,
			Size:      v.Size,
		})
	}
	return resp, nil
}

func toCoordinate(c *envv1.Coordinate) vars.Coordinate {
	return vars.Coordinate{
		Slug:        c.GetSlug(),
		Folder:      c.GetFolder(),
		Key:         c.GetKey(),
		Environment: c.GetEnvironment(),
	}
}

func toCoordinateProto(c vars.Coordinate) *envv1.Coordinate {
	return &envv1.Coordinate{
		Slug:        c.Slug,
		Folder:      c.Folder,
		Key:         c.Key,
		Environment: c.Environment,
	}
}

func toMetadataProto(m vars.Metadata) *envv1.ValueMetadata {
	out := &envv1.ValueMetadata{
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

// varsError classifies a store failure so the CLI can tell a lost race from a
// broken request without matching on message text.
//
// A reference refusal is an invalid argument rather than a failed precondition:
// FAILED_PRECONDITION is how a caller recognises a lost race, and spending it
// on an address the store's shape forbids would tell a user their value changed
// under them when nothing of the sort happened.
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
