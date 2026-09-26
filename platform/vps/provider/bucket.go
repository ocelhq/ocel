package vps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	storeKind     = "s3"
	storeResource = "store"
	storePort     = "9000"
	storeData     = "/data"
	storeRegion   = "us-east-1"

	storeAccessKey = "ocel"
	storeSecretEnv = "RUSTFS_SECRET_KEY"
	storeSecretLen = host.StoreSecretMax / 2

	storeBucketNameMax = 63

	propertySweepUploads = "sweepUploads"
	propertyOrigins      = "allowedOrigins"
)

const storeHealthPath = "/health/ready"

func storeRoute(ref provider.StackRef, store string) host.AppRoute {
	pointer := edge.DefaultPointer
	if ref.Class == edge.ClassPreview {
		pointer = ref.Name.Env
	}
	return host.AppRoute{
		RouteKey: host.RouteKey{
			Owner:   live.Surface(ref.Project, string(ref.Class)),
			Pointer: pointer,
			App:     switchboard.StoreLabel,
		},
		Upstream: store + ":" + storePort,
	}
}

func storeBucketSpec(ref provider.StackRef, store, secret string, spec host.BucketSpec) host.BucketSpec {
	spec.Store = store
	spec.Class = ref.Class
	spec.Endpoint = "http://127.0.0.1:" + storePort
	spec.Region = storeRegion
	spec.AccessKeyID = storeAccessKey
	spec.SecretKey = secret
	return spec
}

func storeContainer(in resources.ProvisionRequest) host.ResourceContainer {
	return host.ResourceContainer{
		Name:     storeName(in.Ref),
		Project:  in.Ref.Project,
		Resource: storeResource,
		Class:    in.Ref.Class,

		Image: constants.ObjectStoreImage(),
		Env: map[string]string{
			"RUSTFS_ACCESS_KEY":     storeAccessKey,
			"RUSTFS_ADDRESS":        ":" + storePort,
			"RUSTFS_CONSOLE_ENABLE": "false",
			"RUSTFS_REGION":         storeRegion,
			"RUSTFS_VOLUMES":        storeData,
		},

		Volume:     host.Volume{Path: storeData},
		Credential: host.Credential{Env: storeSecretEnv},
		Ready:      []string{"curl", "-fsS", "http://127.0.0.1:" + storePort + storeHealthPath},
		Backup:     host.BackupVolume,
	}
}

func storeRef(ref provider.StackRef) provider.StackRef {
	return provider.StackRef{Project: ref.Project, Class: ref.Class, Name: naming.InfraStack(ref.Name.Env)}
}

func storeName(ref provider.StackRef) string {
	return host.ResourceName(storeRef(ref).Name.String(), storeResource, storeKind)
}

var unsafeInBucketName = regexp.MustCompile(`[^a-z0-9]+`)

func storeBucketName(ref provider.StackRef, resource string) string {
	readable := strings.Trim(unsafeInBucketName.ReplaceAllString(
		strings.ToLower(ref.Name.String()+"-"+resource), "-"), "-")
	if len(readable) <= storeBucketNameMax {
		return readable
	}
	sum := sha256.Sum256([]byte(readable))
	suffix := "-" + hex.EncodeToString(sum[:4])
	return strings.Trim(readable[:storeBucketNameMax-len(suffix)], "-") + suffix
}

func mintStoreSecret() (string, error) {
	raw := make([]byte, storeSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint a store credential: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

type storeCredential struct {
	sealed []byte
	secret string
}

type liveStores struct {
	mu       sync.Mutex
	minted   map[string]storeCredential
	dropped  map[string]bool
	sweeps   bool
	spec     *host.ResourceContainer
	shapedBy string
}

func droppedBucket(stack naming.StackName, binding string) string {
	return stack.String() + "/" + binding
}

func (s *liveStores) forgot(stack naming.StackName, binding string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped == nil {
		s.dropped = map[string]bool{}
	}
	s.dropped[droppedBucket(stack, binding)] = true
}

func (s *liveStores) forgotten(stack naming.StackName, binding string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped[droppedBucket(stack, binding)]
}

func (s *liveStores) expiring(state host.BucketState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweeps = s.sweeps || !state.ExpiresUploads
}

func (s *liveStores) sweeping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweeps
}

func (s *liveStores) shaped(resource string, spec host.ResourceContainer) (host.ResourceContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spec == nil {
		s.spec, s.shapedBy = &spec, resource
		return spec, nil
	}
	if !reflect.DeepEqual(*s.spec, spec) {
		return host.ResourceContainer{}, refusal.Refuse(refusal.CodeInvalid,
			"buckets %s and %s shape the one store container differently\n"+
				"Gate the `vps.bucket` rule on the environment, or patch both buckets the same way",
			resource, s.shapedBy)
	}
	return *s.spec, nil
}

func (s *liveStores) shape() *host.ResourceContainer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spec
}

func (s *liveStores) once(name string, mint func() (storeCredential, error)) (storeCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.minted[name]; ok {
		return cached, nil
	}
	credential, err := mint()
	if err != nil {
		return storeCredential{}, err
	}
	if s.minted == nil {
		s.minted = map[string]storeCredential{}
	}
	s.minted[name] = credential
	return credential, nil
}

func storeCoordinate(ref provider.StackRef) records.SealScope {
	return records.SealScope{
		Project: ref.Project, Class: ref.Class, Env: storeRef(ref).Name.String(),
		Folder: live.StoreSecretFolder, Binding: live.StoreSecretBinding, Name: live.StoreSecretName,
	}
}

func (p *Provider) storeCredential(ctx context.Context, ref provider.StackRef, name string) (storeCredential, error) {
	at := storeCoordinate(ref)
	sealed, err := p.host.Kept(ctx, ref.Class, name)
	if err != nil {
		return storeCredential{}, err
	}
	if len(sealed) == 0 {
		minted, err := mintStoreSecret()
		if err != nil {
			return storeCredential{}, err
		}
		candidate, err := p.cipher.Seal(ctx, at, []byte(minted))
		if err != nil {
			return storeCredential{}, err
		}
		if sealed, err = p.host.KeepOnce(ctx, ref.Class, name, candidate); err != nil {
			return storeCredential{}, err
		}
	}
	opened, err := p.cipher.Open(ctx, at, sealed)
	if err != nil {
		return storeCredential{}, err
	}
	if len(opened) == 0 {
		return storeCredential{}, refusal.Refuse(refusal.CodeNotReady,
			"the credential kept for %s is empty\nRemove %s on the box",
			name, host.KeptPath(ref.Class, name))
	}
	return storeCredential{sealed: sealed, secret: string(opened)}, nil
}

func (p *Provider) ProvisionBucket(ctx context.Context, in resources.ProvisionRequest, progress edge.Progress) (provider.Binding, error) {

	spec := storeContainer(in)
	spec, err := p.reshaped(ctx, in, transformTypeBucket, spec)
	if err != nil {
		return provider.Binding{}, err
	}
	spec, err = p.stores.shaped(in.Resource.Name, spec)
	if err != nil {
		return provider.Binding{}, err
	}
	if progress != nil {
		progress.Say("Provisioning bucket " + in.Resource.Name + " in " + spec.Name)
	}

	var sessions host.BucketState
	started := false
	root, err := p.stores.once(spec.Name, func() (storeCredential, error) {
		credential, err := p.storeCredential(ctx, in.Ref, spec.Name)
		if err != nil {
			return storeCredential{}, err
		}
		if err := p.host.RunResource(ctx, spec, credential.secret); err != nil {
			return storeCredential{}, err
		}
		if err := p.host.RouteResource(ctx, storeRoute(in.Ref, spec.Name)); err != nil {
			return storeCredential{}, err
		}
		state, err := p.host.ProvisionBucket(ctx, storeBucketSpec(in.Ref, spec.Name, credential.secret,
			host.BucketSpec{Bucket: constants.StoreSessionsBucket(), Internal: true}))
		if err != nil {
			return storeCredential{}, err
		}
		sessions, started = state, true
		return credential, nil
	})
	if err != nil {
		return provider.Binding{}, err
	}
	if started {
		p.stores.expiring(sessions)
	}

	origins, err := p.bucketOrigins(ctx, in.Ref, in.Resource.Bucket)
	if err != nil {
		return provider.Binding{}, err
	}
	public := declaredPublic(in.Resource.Bucket)
	bucket := storeBucketName(in.Ref, in.Resource.Name)
	state, err := p.host.ProvisionBucket(ctx, storeBucketSpec(in.Ref, spec.Name, root.secret, host.BucketSpec{
		Bucket:         bucket,
		AllowedOrigins: origins,
		Public:         public,
	}))
	if err != nil {
		return provider.Binding{}, err
	}
	p.stores.expiring(state)

	return provider.Binding{
		Type:     provider.BindingBucket,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyBucket: bucket,
			provider.PropertyPublic: strconv.FormatBool(public),
			propertySweepUploads:    strconv.FormatBool(p.stores.sweeping()),
			propertyOrigins:         strings.Join(declaredOrigins(in.Resource.Bucket), " "),
		},
	}, nil
}

func (p *Provider) storeSection(ctx context.Context, spec provider.StackSpec) (*live.Store, error) {
	if !slices.ContainsFunc(spec.App.Values.Bindings, func(binding provider.Binding) bool {
		return binding.Type == provider.BindingBucket && !binding.Endpointed()
	}) {
		return nil, nil
	}
	container := p.stores.shape()
	if container == nil {
		shaped := storeContainer(resources.ProvisionRequest{Ref: storeRef(spec.Ref)})
		container = &shaped
	}
	root, err := p.stores.once(container.Name, func() (storeCredential, error) {
		return p.storeCredential(ctx, spec.Ref, container.Name)
	})
	if err != nil {
		return nil, err
	}
	own, err := p.storeAccount(ctx, spec, container.Name, root)
	if err != nil {
		return nil, err
	}
	return &live.Store{
		Env:          storeRef(spec.Ref).Name.String(),
		Endpoint:     "http://" + container.Name + ":" + storePort,
		Region:       storeRegion,
		AccessKeyID:  own.key,
		PathStyle:    true,
		Pointer:      storeRoute(spec.Ref, container.Name).Pointer,
		Volume:       container.VolumeName(),
		Sessions:     sessionsPrefix(spec),
		Granted:      grantedBuckets(spec.App),
		SweepUploads: sweptUploads(spec.App),
		Sealed:       base64.StdEncoding.EncodeToString(own.credential.sealed),
	}, nil
}

func sessionsPrefix(spec provider.StackSpec) string {
	return constants.StoreSessionsBucket() + "/" +
		storeRef(spec.Ref).Name.String() + "/" + naming.Sanitize(appNameOf(spec.App))
}

type appAccount struct {
	key        string
	credential storeCredential
}

func (p *Provider) storeAccount(ctx context.Context, spec provider.StackSpec, store string, root storeCredential) (appAccount, error) {
	env := storeRef(spec.Ref).Name.String()
	key := host.StoreAccountKey(env, appNameOf(spec.App))
	credential, err := p.stores.once(key, func() (storeCredential, error) {
		return p.storeCredential(ctx, spec.Ref, key)
	})
	if err != nil {
		return appAccount{}, err
	}
	if err := p.host.GrantStoreAccount(ctx, host.StoreAccount{
		Store:       store,
		Class:       spec.Ref.Class,
		Endpoint:    "http://127.0.0.1:" + storePort,
		Region:      storeRegion,
		RootKeyID:   storeAccessKey,
		RootSecret:  root.secret,
		AccessKeyID: key,
		SecretKey:   credential.secret,
		Buckets:     boundBuckets(spec.App),
		Sessions:    sessionsPrefix(spec),
	}); err != nil {
		return appAccount{}, err
	}
	return appAccount{key: key, credential: credential}, nil
}

func appNameOf(app *provider.AppSpec) string {
	if app == nil {
		return ""
	}
	return app.App
}

func grantedBuckets(app *provider.AppSpec) []string {
	if app == nil {
		return nil
	}
	var granted []string
	for _, binding := range append(slices.Clone(app.Values.Bindings), app.Grants...) {
		if binding.Type != provider.BindingBucket || binding.Endpointed() {
			continue
		}
		if spec := binding.Properties[provider.PropertyBucket]; spec != "" && !slices.Contains(granted, spec) {
			granted = append(granted, spec)
		}
	}
	return granted
}

func sweptUploads(app *provider.AppSpec) bool {
	if app == nil {
		return false
	}
	return slices.ContainsFunc(append(slices.Clone(app.Values.Bindings), app.Grants...),
		func(binding provider.Binding) bool {
			return binding.Type == provider.BindingBucket &&
				binding.Properties[propertySweepUploads] == "true"
		})
}

func boundBuckets(app *provider.AppSpec) []string {
	var bound []string
	for _, spec := range grantedBuckets(app) {
		bucket, _, _ := strings.Cut(spec, "/")
		if bucket != "" && !slices.Contains(bound, bucket) {
			bound = append(bound, bucket)
		}
	}
	return bound
}

func declaredOrigins(spec *provider.BucketSpec) []string {
	if spec == nil {
		return nil
	}
	return spec.AllowedOrigins
}

func (p *Provider) bucketOrigins(ctx context.Context, ref provider.StackRef, spec *provider.BucketSpec) ([]string, error) {
	claims, err := p.host.Claims(ctx)
	if err != nil {
		return nil, err
	}
	return corsOrigins(ref, declaredOrigins(spec), claims), nil
}

func corsOrigins(ref provider.StackRef, declared []string, claims []host.HostClaim) []string {
	origins := slices.Clone(declared)
	owner := live.Surface(ref.Project, string(ref.Class))
	for _, claim := range claims {
		if claim.Owner != owner || claim.App == switchboard.StoreLabel {
			continue
		}
		if origin := "https://" + claim.Hostname; !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	return origins
}

func (p *Provider) applyOrigins(ctx context.Context, project string, class edge.Class) error {
	entries, err := stackrecords.List(ctx, p.records, class, project)
	if err != nil {
		return err
	}
	claims := sync.OnceValues(func() ([]host.HostClaim, error) { return p.host.Claims(ctx) })
	for _, entry := range entries {
		ref := provider.StackRef{Project: project, Class: class, Name: entry.Name}
		if err := p.applyStackOrigins(ctx, ref, entry, claims); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) applyStackOrigins(ctx context.Context, ref provider.StackRef, entry stackrecords.NamedStack, claims func() ([]host.HostClaim, error)) error {
	buckets := slices.DeleteFunc(slices.Clone(entry.Bindings), func(binding provider.Binding) bool {
		return binding.Type != provider.BindingBucket || p.stores.forgotten(entry.Name, binding.Name)
	})
	if len(buckets) == 0 {
		return nil
	}
	claimed, err := claims()
	if err != nil {
		return err
	}
	root, err := p.storeRoot(ctx, ref, storeName(ref))
	if err != nil || root.secret == "" {
		return err
	}
	for _, binding := range buckets {
		if err := p.host.ApplyOrigins(ctx, storeBucketSpec(ref, storeName(ref), root.secret, host.BucketSpec{
			Bucket:         binding.Properties[provider.PropertyBucket],
			AllowedOrigins: corsOrigins(ref, recordedOrigins(binding), claimed),
		})); err != nil {
			return err
		}
	}
	return nil
}

func recordedOrigins(binding provider.Binding) []string {
	return strings.Fields(binding.Properties[propertyOrigins])
}

func declaredPublic(spec *provider.BucketSpec) bool {
	return spec != nil && spec.Public
}

func (p *Provider) removeBucket(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress edge.Progress) error {
	if err := p.dropBucket(ctx, ref, binding, progress); err != nil {
		return err
	}
	p.stores.forgot(ref.Name, binding.Name)
	return p.reconcileStore(ctx, ref, progress)
}

func (p *Provider) dropBucket(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress edge.Progress) error {
	store := storeName(ref)
	root, err := p.storeRoot(ctx, ref, store)
	if err != nil {
		return err
	}
	if root.secret == "" {
		if progress != nil {
			progress.Say("Leaving bucket " + binding.Name + ": no credential kept for " + store)
		}
		return nil
	}
	bucket := binding.Properties[provider.PropertyBucket]
	if bucket == "" {
		bucket = storeBucketName(ref, binding.Name)
	}
	if progress != nil {
		progress.Say("Taking bucket " + binding.Name + " and its objects down")
	}
	return p.host.RemoveBucket(ctx, host.BucketRef{
		Class:       ref.Class,
		Project:     ref.Project,
		Store:       store,
		Bucket:      bucket,
		Endpoint:    "http://127.0.0.1:" + storePort,
		Region:      storeRegion,
		AccessKeyID: storeAccessKey,
		SecretKey:   root.secret,
	})
}

func (p *Provider) reconcileStore(ctx context.Context, ref provider.StackRef, progress edge.Progress) error {
	last, err := p.lastBucket(ctx, ref)
	if err != nil || !last {
		return err
	}
	return p.removeStore(ctx, ref, progress)
}

func (p *Provider) lastBucket(ctx context.Context, ref provider.StackRef) (bool, error) {
	entries, err := stackrecords.List(ctx, p.records, ref.Class, ref.Project)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name.Env != ref.Name.Env {
			continue
		}
		for _, binding := range entry.Bindings {
			if binding.Type != provider.BindingBucket || p.stores.forgotten(entry.Name, binding.Name) {
				continue
			}
			return false, nil
		}
	}
	return true, nil
}

func (p *Provider) removeStore(ctx context.Context, ref provider.StackRef, progress edge.Progress) error {
	store := storeName(ref)
	if progress != nil {
		progress.Say("Taking the store " + store + " down")
	}
	if err := p.host.UnrouteApp(ctx, storeRoute(ref, store).RouteKey); err != nil {
		return err
	}
	if err := p.host.RemoveResource(ctx, host.ResourceRef{
		Class: ref.Class, Project: ref.Project, Resource: storeResource, Name: store,
	}); err != nil {
		return err
	}
	accounts, err := p.storeAccounts(ctx, ref)
	if err != nil {
		return err
	}
	return p.host.ForgetKept(ctx, ref.Class, accounts)
}

func (p *Provider) storeAccounts(ctx context.Context, ref provider.StackRef) ([]string, error) {
	entries, err := stackrecords.List(ctx, p.records, ref.Class, ref.Project)
	if err != nil {
		return nil, err
	}
	env := storeRef(ref).Name.String()
	var accounts []string
	for _, entry := range entries {
		if entry.Name.Env != ref.Name.Env {
			continue
		}
		app := entry.App
		if app == "" {
			app = entry.Name.App
		}
		if app == "" || app == naming.InfraApp {
			continue
		}
		if key := host.StoreAccountKey(env, app); !slices.Contains(accounts, key) {
			accounts = append(accounts, key)
		}
	}
	return accounts, nil
}

func (p *Provider) removeStoreAccount(ctx context.Context, ref provider.StackRef, app string) error {
	store := storeName(ref)
	key := host.StoreAccountKey(storeRef(ref).Name.String(), app)
	sealed, err := p.host.Kept(ctx, ref.Class, key)
	if err != nil || len(sealed) == 0 {
		return err
	}
	root, err := p.storeRoot(ctx, ref, store)
	if err != nil {
		return err
	}
	if root.secret != "" {
		if err := p.host.RevokeStoreAccount(ctx, host.StoreAccount{
			Store:       store,
			Class:       ref.Class,
			Endpoint:    "http://127.0.0.1:" + storePort,
			Region:      storeRegion,
			RootKeyID:   storeAccessKey,
			RootSecret:  root.secret,
			AccessKeyID: key,
		}); err != nil {
			return err
		}
	}
	return p.host.ForgetKept(ctx, ref.Class, []string{key})
}

func (p *Provider) storeRoot(ctx context.Context, ref provider.StackRef, store string) (storeCredential, error) {
	sealed, err := p.host.Kept(ctx, ref.Class, store)
	if err != nil {
		return storeCredential{}, err
	}
	if len(sealed) == 0 {
		return storeCredential{}, nil
	}
	opened, err := p.cipher.Open(ctx, storeCoordinate(ref), sealed)
	if err != nil {
		return storeCredential{}, err
	}
	return storeCredential{sealed: sealed, secret: string(opened)}, nil
}
