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
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
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

func storeRoute(ref providerkit.StackRef, store string) host.AppRoute {
	pointer := edge.DefaultPointer
	if ref.Class == providerkit.ClassPreview {
		pointer = ref.Name.Env
	}
	return host.AppRoute{
		RouteKey: host.RouteKey{
			Owner:   live.Surface(ref.Project, string(ref.Class)),
			Pointer: pointer,
			App:     live.StoreLabel,
		},
		Upstream: store + ":" + storePort,
		Health:   storeHealthPath,
	}
}

func storeBucketSpec(ref providerkit.StackRef, store, secret string, spec host.BucketSpec) host.BucketSpec {
	spec.Store = store
	spec.Class = ref.Class
	spec.Endpoint = "http://127.0.0.1:" + storePort
	spec.Region = storeRegion
	spec.AccessKeyID = storeAccessKey
	spec.SecretKey = secret
	return spec
}

func storeContainer(in resources.Instruction) host.ResourceContainer {
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

func storeRef(ref providerkit.StackRef) providerkit.StackRef {
	return providerkit.StackRef{Project: ref.Project, Class: ref.Class, Name: naming.InfraStack(ref.Name.Env)}
}

func storeName(ref providerkit.StackRef) string {
	return host.ResourceName(storeRef(ref).Name.String(), storeResource, storeKind)
}

var unsafeInBucketName = regexp.MustCompile(`[^a-z0-9]+`)

func storeBucketName(ref providerkit.StackRef, resource string) string {
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

type standingStores struct {
	mu      sync.Mutex
	stood   map[string]storeCredential
	dropped map[string]bool
	sweeps  bool
	spec    *host.ResourceContainer
	shaper  string
	probe   host.StoreProbe
}

func droppedBucket(stack naming.StackName, binding string) string {
	return stack.String() + "/" + binding
}

func (s *standingStores) forgot(stack naming.StackName, binding string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped == nil {
		s.dropped = map[string]bool{}
	}
	s.dropped[droppedBucket(stack, binding)] = true
}

func (s *standingStores) forgotten(stack naming.StackName, binding string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped[droppedBucket(stack, binding)]
}

func (s *standingStores) expiring(standing host.BucketStanding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweeps = s.sweeps || !standing.ExpiresUploads
}

func (s *standingStores) sweeping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweeps
}

func (s *standingStores) probed(probe host.StoreProbe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probe = probe
}

func (s *standingStores) probing() host.StoreProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probe
}

func (s *standingStores) shaped(resource string, spec host.ResourceContainer) (host.ResourceContainer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spec == nil {
		s.spec, s.shaper = &spec, resource
		return spec, nil
	}
	if !reflect.DeepEqual(*s.spec, spec) {
		return host.ResourceContainer{}, providerkit.Refuse(providerkit.CodeInvalid,
			"buckets %s and %s shape the one store container differently\n"+
				"Gate the `vps.bucket` rule on the environment, or patch both buckets the same way",
			resource, s.shaper)
	}
	return *s.spec, nil
}

func (s *standingStores) shape() *host.ResourceContainer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spec
}

func (s *standingStores) once(name string, stand func() (storeCredential, error)) (storeCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, stood := s.stood[name]; stood {
		return held, nil
	}
	held, err := stand()
	if err != nil {
		return storeCredential{}, err
	}
	if s.stood == nil {
		s.stood = map[string]storeCredential{}
	}
	s.stood[name] = held
	return held, nil
}

func storeCoordinate(ref providerkit.StackRef) providerkit.Coordinate {
	return providerkit.Coordinate{
		Project: ref.Project, Class: ref.Class, Env: storeRef(ref).Name.String(),
		Folder: live.StoreSecretFolder, Binding: live.StoreSecretBinding, Name: live.StoreSecretName,
	}
}

func (p *Provider) storeCredential(ctx context.Context, ref providerkit.StackRef, name string) (storeCredential, error) {
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
		candidate, err := p.sealer.Seal(ctx, at, []byte(minted))
		if err != nil {
			return storeCredential{}, err
		}
		if sealed, err = p.host.KeepOnce(ctx, ref.Class, name, candidate); err != nil {
			return storeCredential{}, err
		}
	}
	opened, err := p.sealer.Open(ctx, at, sealed)
	if err != nil {
		return storeCredential{}, err
	}
	if len(opened) == 0 {
		return storeCredential{}, providerkit.Refuse(providerkit.CodeNotReady,
			"the credential kept for %s is empty\nRemove %s on the box",
			name, host.KeptPath(ref.Class, name))
	}
	return storeCredential{sealed: sealed, secret: string(opened)}, nil
}

func (p *Provider) Bucket(ctx context.Context, in resources.Instruction, report providerkit.Reporter) (providerkit.Binding, error) {
	if external := p.options.Bucket; external.configured() {
		return p.externalBucket(in, report)
	}

	spec := storeContainer(in)
	spec, err := p.reshaped(ctx, in, transformTypeBucket, spec)
	if err != nil {
		return providerkit.Binding{}, err
	}
	spec, err = p.stores.shaped(in.Resource.Name, spec)
	if err != nil {
		return providerkit.Binding{}, err
	}
	if report != nil {
		report.Say("Standing bucket " + in.Resource.Name + " up in " + spec.Name)
	}

	var sessions host.BucketStanding
	stood := false
	held, err := p.stores.once(spec.Name, func() (storeCredential, error) {
		held, err := p.storeCredential(ctx, in.Ref, spec.Name)
		if err != nil {
			return storeCredential{}, err
		}
		if err := p.host.StandResource(ctx, spec, held.secret); err != nil {
			return storeCredential{}, err
		}
		if err := p.host.RouteResource(ctx, storeRoute(in.Ref, spec.Name)); err != nil {
			return storeCredential{}, err
		}
		standing, err := p.host.ProvisionBucket(ctx, storeBucketSpec(in.Ref, spec.Name, held.secret,
			host.BucketSpec{Bucket: constants.StoreSessionsBucket(), Internal: true}))
		if err != nil {
			return storeCredential{}, err
		}
		sessions, stood = standing, true
		return held, nil
	})
	if err != nil {
		return providerkit.Binding{}, err
	}
	if stood {
		p.stores.expiring(sessions)
	}

	origins, err := p.bucketOrigins(ctx, in.Ref, in.Resource.Bucket)
	if err != nil {
		return providerkit.Binding{}, err
	}
	public := declaredPublic(in.Resource.Bucket)
	bucket := storeBucketName(in.Ref, in.Resource.Name)
	standing, err := p.host.ProvisionBucket(ctx, storeBucketSpec(in.Ref, spec.Name, held.secret, host.BucketSpec{
		Bucket:         bucket,
		AllowedOrigins: origins,
		Public:         public,
	}))
	if err != nil {
		return providerkit.Binding{}, err
	}
	p.stores.expiring(standing)

	return providerkit.Binding{
		Type:     providerkit.BindingBucket,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			providerkit.PropertyBucket: bucket,
			providerkit.PropertyPublic: strconv.FormatBool(public),
			propertySweepUploads:       strconv.FormatBool(p.stores.sweeping()),
			propertyOrigins:            strings.Join(declaredOrigins(in.Resource.Bucket), " "),
		},
	}, nil
}

func (p *Provider) externalBucket(in resources.Instruction, report providerkit.Reporter) (providerkit.Binding, error) {
	if declaredPublic(in.Resource.Bucket) {
		return providerkit.Binding{}, providerkit.Refuse(providerkit.CodeInvalid,
			"bucket %s is public, but option %q names an external store ocel cannot open\nDrop `public` from %s, or serve it through your app or a signed url",
			in.Resource.Name, "bucket", in.Resource.Name)
	}
	prefix := naming.Sanitize(in.Ref.Project) + "/" + naming.Sanitize(in.Ref.Name.Env) + "/" + naming.Sanitize(in.Resource.Name)
	if report != nil {
		report.Say("Binding bucket " + in.Resource.Name + " to " + p.options.Bucket.Bucket + "/" + prefix)
	}
	return providerkit.Binding{
		Type:     providerkit.BindingBucket,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			providerkit.PropertyBucket: p.options.Bucket.Bucket + "/" + prefix,
		},
	}, nil
}

func (p *Provider) storeSection(ctx context.Context, plan providerkit.StackPlan) (*live.Store, error) {
	if !slices.ContainsFunc(plan.App.Values.Bindings, func(binding providerkit.Binding) bool {
		return binding.Type == providerkit.BindingBucket
	}) {
		return nil, nil
	}
	if external := p.options.Bucket; external.configured() {
		sealed, err := p.sealer.Seal(ctx, storeCoordinate(plan.Ref), []byte(external.SecretAccessKey))
		if err != nil {
			return nil, err
		}
		return &live.Store{
			Env:           storeRef(plan.Ref).Name.String(),
			Endpoint:      external.Endpoint,
			PublicBaseURL: external.Endpoint,
			Region:        external.Region,
			AccessKeyID:   external.AccessKeyID,
			PathStyle:     external.PathStyle,
			Sessions: external.Bucket + "/" + naming.Sanitize(plan.Ref.Project) + "/" +
				naming.Sanitize(plan.Ref.Name.Env) + "/" + naming.Sanitize(appNameOf(plan.App)),
			Granted:      grantedBuckets(plan.App),
			PostPolicies: p.stores.probing().PostPolicies,
			SweepUploads: true,
			Sealed:       base64.StdEncoding.EncodeToString(sealed),
		}, nil
	}
	spec := p.stores.shape()
	if spec == nil {
		shaped := storeContainer(resources.Instruction{Ref: storeRef(plan.Ref)})
		spec = &shaped
	}
	held, err := p.stores.once(spec.Name, func() (storeCredential, error) {
		return p.storeCredential(ctx, plan.Ref, spec.Name)
	})
	if err != nil {
		return nil, err
	}
	own, err := p.storeAccount(ctx, plan, spec.Name, held)
	if err != nil {
		return nil, err
	}
	return &live.Store{
		Env:          storeRef(plan.Ref).Name.String(),
		Endpoint:     "http://" + spec.Name + ":" + storePort,
		Region:       storeRegion,
		AccessKeyID:  own.key,
		PathStyle:    true,
		Pointer:      storeRoute(plan.Ref, spec.Name).Pointer,
		Volume:       spec.VolumeName(),
		Sessions:     sessionsPrefix(plan),
		Granted:      grantedBuckets(plan.App),
		PostPolicies: true,
		SweepUploads: sweptUploads(plan.App),
		Sealed:       base64.StdEncoding.EncodeToString(own.held.sealed),
	}, nil
}

func sessionsPrefix(plan providerkit.StackPlan) string {
	return constants.StoreSessionsBucket() + "/" +
		storeRef(plan.Ref).Name.String() + "/" + naming.Sanitize(appNameOf(plan.App))
}

type appAccount struct {
	key  string
	held storeCredential
}

func (p *Provider) storeAccount(ctx context.Context, plan providerkit.StackPlan, store string, root storeCredential) (appAccount, error) {
	env := storeRef(plan.Ref).Name.String()
	key := host.StoreAccountKey(env, appNameOf(plan.App))
	held, err := p.stores.once(key, func() (storeCredential, error) {
		return p.storeCredential(ctx, plan.Ref, key)
	})
	if err != nil {
		return appAccount{}, err
	}
	if err := p.host.GrantStoreAccount(ctx, host.StoreAccount{
		Store:       store,
		Class:       plan.Ref.Class,
		Endpoint:    "http://127.0.0.1:" + storePort,
		Region:      storeRegion,
		RootKeyID:   storeAccessKey,
		RootSecret:  root.secret,
		AccessKeyID: key,
		SecretKey:   held.secret,
		Buckets:     boundBuckets(plan.App),
		Sessions:    sessionsPrefix(plan),
	}); err != nil {
		return appAccount{}, err
	}
	return appAccount{key: key, held: held}, nil
}

func appNameOf(app *providerkit.AppPlan) string {
	if app == nil {
		return ""
	}
	return app.App
}

func grantedBuckets(app *providerkit.AppPlan) []string {
	if app == nil {
		return nil
	}
	var held []string
	for _, binding := range append(slices.Clone(app.Values.Bindings), app.Grants...) {
		if binding.Type != providerkit.BindingBucket {
			continue
		}
		if spec := binding.Properties[providerkit.PropertyBucket]; spec != "" && !slices.Contains(held, spec) {
			held = append(held, spec)
		}
	}
	return held
}

func sweptUploads(app *providerkit.AppPlan) bool {
	if app == nil {
		return false
	}
	return slices.ContainsFunc(append(slices.Clone(app.Values.Bindings), app.Grants...),
		func(binding providerkit.Binding) bool {
			return binding.Type == providerkit.BindingBucket &&
				binding.Properties[propertySweepUploads] == "true"
		})
}

func boundBuckets(app *providerkit.AppPlan) []string {
	var held []string
	for _, spec := range grantedBuckets(app) {
		bucket, _, _ := strings.Cut(spec, "/")
		if bucket != "" && !slices.Contains(held, bucket) {
			held = append(held, bucket)
		}
	}
	return held
}

func declaredOrigins(spec *providerkit.BucketSpec) []string {
	if spec == nil {
		return nil
	}
	return spec.AllowedOrigins
}

func (p *Provider) bucketOrigins(ctx context.Context, ref providerkit.StackRef, spec *providerkit.BucketSpec) ([]string, error) {
	claims, err := p.host.Claims(ctx)
	if err != nil {
		return nil, err
	}
	return answered(ref, declaredOrigins(spec), claims), nil
}

func answered(ref providerkit.StackRef, declared []string, claims []host.HostClaim) []string {
	origins := slices.Clone(declared)
	owner := live.Surface(ref.Project, string(ref.Class))
	for _, claim := range claims {
		if claim.Owner != owner || claim.App == live.StoreLabel {
			continue
		}
		if origin := "https://" + claim.Hostname; !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	return origins
}

func (p *Provider) holdOrigins(ctx context.Context, project string, class providerkit.Class) error {
	if p.options.Bucket.configured() {
		return nil
	}
	entries, err := providerkit.ReadStacks(ctx, p.records, class, project)
	if err != nil {
		return err
	}
	var claims []host.HostClaim
	claimed := false
	for _, entry := range entries {
		ref := providerkit.StackRef{Project: project, Class: class, Name: entry.Name}
		var root storeCredential
		opened := false
		for _, binding := range entry.Bindings {
			if binding.Type != providerkit.BindingBucket || p.stores.forgotten(entry.Name, binding.Name) {
				continue
			}
			if !claimed {
				if claims, err = p.host.Claims(ctx); err != nil {
					return err
				}
				claimed = true
			}
			if !opened {
				if root, err = p.storeRoot(ctx, ref, storeName(ref)); err != nil {
					return err
				}
				opened = true
			}
			if root.secret == "" {
				break
			}
			if err := p.host.HoldOrigins(ctx, storeBucketSpec(ref, storeName(ref), root.secret, host.BucketSpec{
				Bucket:         binding.Properties[providerkit.PropertyBucket],
				AllowedOrigins: answered(ref, recordedOrigins(binding), claims),
			})); err != nil {
				return err
			}
		}
	}
	return nil
}

func recordedOrigins(binding providerkit.Binding) []string {
	return strings.Fields(binding.Properties[propertyOrigins])
}

func declaredPublic(spec *providerkit.BucketSpec) bool {
	return spec != nil && spec.Public
}

func (p *Provider) removeBucket(ctx context.Context, ref providerkit.StackRef, binding providerkit.Binding, report providerkit.Reporter) error {
	if external := p.options.Bucket; external.configured() {
		if report != nil {
			report.Say("Leaving bucket " + binding.Name + "'s objects under " +
				binding.Properties[providerkit.PropertyBucket])
		}
		return nil
	}
	if err := p.dropBucket(ctx, ref, binding, report); err != nil {
		return err
	}
	p.stores.forgot(ref.Name, binding.Name)
	return p.reconcileStore(ctx, ref, report)
}

func (p *Provider) dropBucket(ctx context.Context, ref providerkit.StackRef, binding providerkit.Binding, report providerkit.Reporter) error {
	store := storeName(ref)
	held, err := p.storeRoot(ctx, ref, store)
	if err != nil {
		return err
	}
	if held.secret == "" {
		if report != nil {
			report.Say("Leaving bucket " + binding.Name + ": no credential kept for " + store)
		}
		return nil
	}
	bucket := binding.Properties[providerkit.PropertyBucket]
	if bucket == "" {
		bucket = storeBucketName(ref, binding.Name)
	}
	if report != nil {
		report.Say("Taking bucket " + binding.Name + " and its objects down")
	}
	return p.host.RemoveBucket(ctx, host.BucketRef{
		Class:       ref.Class,
		Project:     ref.Project,
		Store:       store,
		Bucket:      bucket,
		Endpoint:    "http://127.0.0.1:" + storePort,
		Region:      storeRegion,
		AccessKeyID: storeAccessKey,
		SecretKey:   held.secret,
	})
}

func (p *Provider) reconcileStore(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	last, err := p.lastBucket(ctx, ref)
	if err != nil || !last {
		return err
	}
	return p.removeStore(ctx, ref, report)
}

func (p *Provider) lastBucket(ctx context.Context, ref providerkit.StackRef) (bool, error) {
	entries, err := providerkit.ReadStacks(ctx, p.records, ref.Class, ref.Project)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name.Env != ref.Name.Env {
			continue
		}
		for _, held := range entry.Bindings {
			if held.Type != providerkit.BindingBucket || p.stores.forgotten(entry.Name, held.Name) {
				continue
			}
			return false, nil
		}
	}
	return true, nil
}

func (p *Provider) removeStore(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	store := storeName(ref)
	if report != nil {
		report.Say("Taking the store " + store + " down")
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

func (p *Provider) storeAccounts(ctx context.Context, ref providerkit.StackRef) ([]string, error) {
	entries, err := providerkit.ReadStacks(ctx, p.records, ref.Class, ref.Project)
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

func (p *Provider) removeStoreAccount(ctx context.Context, ref providerkit.StackRef, app string) error {
	if external := p.options.Bucket; external.configured() {
		return nil
	}
	store := storeName(ref)
	key := host.StoreAccountKey(storeRef(ref).Name.String(), app)
	sealed, err := p.host.Kept(ctx, ref.Class, key)
	if err != nil || len(sealed) == 0 {
		return err
	}
	held, err := p.storeRoot(ctx, ref, store)
	if err != nil {
		return err
	}
	if held.secret != "" {
		if err := p.host.RevokeStoreAccount(ctx, host.StoreAccount{
			Store:       store,
			Class:       ref.Class,
			Endpoint:    "http://127.0.0.1:" + storePort,
			Region:      storeRegion,
			RootKeyID:   storeAccessKey,
			RootSecret:  held.secret,
			AccessKeyID: key,
		}); err != nil {
			return err
		}
	}
	return p.host.ForgetKept(ctx, ref.Class, []string{key})
}

func (p *Provider) storeRoot(ctx context.Context, ref providerkit.StackRef, store string) (storeCredential, error) {
	sealed, err := p.host.Kept(ctx, ref.Class, store)
	if err != nil {
		return storeCredential{}, err
	}
	if len(sealed) == 0 {
		return storeCredential{}, nil
	}
	opened, err := p.sealer.Open(ctx, storeCoordinate(ref), sealed)
	if err != nil {
		return storeCredential{}, err
	}
	return storeCredential{sealed: sealed, secret: string(opened)}, nil
}

var _ resources.Bucket = (*Provider)(nil)
