package bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	s3store "github.com/ocelhq/ocel/platform/s3"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

const (
	storeKind     = "s3"
	storeResource = "store"
	storePort     = "9000"
	storeData     = "/data"
	storeRegion   = "us-east-1"

	storeAccessKey = "ocel"
	storeSecretEnv = "RUSTFS_SECRET_KEY"
	secretLen      = secretMax / 2

	storeBucketNameMax = 63
)

const storeHealthPath = "/health/ready"

var storeShape = component.Shape{Kind: "bucket", OwnEnv: []string{
	"RUSTFS_ACCESS_KEY", "RUSTFS_ACCESS_KEY_FILE",
	"RUSTFS_ADDRESS",
	"RUSTFS_REGION",
	"RUSTFS_ROOT_PASSWORD", "RUSTFS_ROOT_USER",
	"RUSTFS_SECRET_KEY", "RUSTFS_SECRET_KEY_FILE",
	"RUSTFS_VOLUMES",
}}

type Resource struct {
	box     component.Box
	stores  *Stores
	objects objectStore
}

func New(box component.Box, stores *Stores) Resource {
	return Resource{box: box, stores: stores, objects: objectStore{box.SSH}}
}

func storeRoute(ref provider.StackRef, store string) boxapi.Route {
	pointer := router.DefaultPointer
	if ref.Tier == environment.TierPreview {
		pointer = ref.Name.Env
	}
	return boxapi.Route{
		RouteKey: boxapi.RouteKey{
			Owner:   boxapi.Surface(ref.Project, string(ref.Tier)),
			Pointer: pointer,
			App:     boxapi.StoreLabel,
		},
		Upstream: store + ":" + storePort,
	}
}

func storeBucketSpec(ref provider.StackRef, store, secret string, spec Spec) Spec {
	spec.Store = store
	spec.Tier = ref.Tier
	spec.Endpoint = "http://127.0.0.1:" + storePort
	spec.Region = storeRegion
	spec.AccessKeyID = storeAccessKey
	spec.SecretKey = secret
	return spec
}

func NewStoreContainer(ref provider.StackRef) (container.Spec, error) {
	name, err := StoreName(ref)
	if err != nil {
		return container.Spec{}, err
	}
	return container.Spec{
		Name:     name,
		Project:  ref.Project,
		Resource: storeResource,
		Tier:     ref.Tier,

		Image: images.ObjectStore(),
		Env: map[string]string{
			"RUSTFS_ACCESS_KEY":     storeAccessKey,
			"RUSTFS_ADDRESS":        ":" + storePort,
			"RUSTFS_CONSOLE_ENABLE": "false",
			"RUSTFS_REGION":         storeRegion,
			"RUSTFS_VOLUMES":        storeData,
		},

		Volume:     container.Volume{Path: storeData},
		Credential: container.Credential{Env: storeSecretEnv},
		Ready:      []string{"curl", "-fsS", "http://127.0.0.1:" + storePort + storeHealthPath},
		Backup:     boxapi.BackupStore,
	}, nil
}

func storeRef(ref provider.StackRef) provider.StackRef {
	return provider.StackRef{Project: ref.Project, Tier: ref.Tier, Name: naming.InfraStack(ref.Name.Env)}
}

func StoreName(ref provider.StackRef) (string, error) {
	return container.Name(storeRef(ref), storeResource, storeKind)
}

func storeBucketName(ref provider.StackRef, resource string) (string, error) {
	stack := ref.Name.String()
	return naming.TaggedName{
		Readable: naming.Sanitize(stack + naming.WordSeparator + resource),
		Tag:      naming.HashTag(stack, resource),
	}.Fit(storeBucketNameMax)
}

func MintSecret() (string, error) {
	raw := make([]byte, secretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint a store credential: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

type storeCredential struct {
	sealed []byte
	secret string
}

func NewSecretAssociatedData(ref provider.StackRef) (seal.AssociatedData, error) {
	return boxapi.NewStoreSecretAssociatedData(ref.Project, ref.Tier, storeRef(ref).Name.String())
}

func (r Resource) storeCredential(ctx context.Context, ref provider.StackRef, name string) (storeCredential, error) {
	bound, err := NewSecretAssociatedData(ref)
	if err != nil {
		return storeCredential{}, err
	}
	sealed, err := r.box.Containers.Kept(ctx, ref.Tier, name)
	if err != nil {
		return storeCredential{}, err
	}
	if len(sealed) == 0 {
		minted, err := MintSecret()
		if err != nil {
			return storeCredential{}, err
		}
		candidate, err := r.box.Cipher.Seal(ctx, ref.Tier, bound, []byte(minted))
		if err != nil {
			return storeCredential{}, err
		}
		if sealed, err = r.box.Containers.KeepOnce(ctx, ref.Tier, name, candidate); err != nil {
			return storeCredential{}, err
		}
	}
	opened, err := r.box.Cipher.Open(ctx, ref.Tier, bound, sealed)
	if err != nil {
		return storeCredential{}, err
	}
	if len(opened) == 0 {
		return storeCredential{}, refusal.Refuse(refusal.CodeNotReady,
			"the credential kept for %s is empty\nRemove %s on the box",
			name, container.KeptPath(ref.Tier, name))
	}
	return storeCredential{sealed: sealed, secret: string(opened)}, nil
}

func (r Resource) shapeStore(ctx context.Context, in resources.ProvisionRequest) (container.Spec, error) {
	spec, err := NewStoreContainer(in.Ref)
	if err != nil {
		return container.Spec{}, err
	}
	return r.box.Reshaped(ctx, in, storeShape, spec)
}

func newSessionsBucketSpec(ref provider.StackRef, store, secret string) Spec {
	return storeBucketSpec(ref, store, secret, Spec{Bucket: s3store.SessionsBucket(), Internal: true})
}

func (r Resource) readDeclaredBucketSpec(ctx context.Context, in resources.ProvisionRequest, store, secret string) (Spec, error) {
	origins, err := r.bucketOrigins(ctx, in.Ref, in.Resource.Bucket)
	if err != nil {
		return Spec{}, err
	}
	bucket, err := storeBucketName(in.Ref, in.Resource.Name)
	if err != nil {
		return Spec{}, err
	}
	return storeBucketSpec(in.Ref, store, secret, Spec{
		Bucket:         bucket,
		AllowedOrigins: origins,
		Public:         declaredPublic(in.Resource.Bucket),
	}), nil
}

func newBucketBinding(in resources.ProvisionRequest, spec Spec, sweeps bool) provider.Binding {
	return provider.Binding{
		Type:     provider.BindingBucket,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyBucket:             spec.Bucket,
			provider.PropertyPublic:             strconv.FormatBool(spec.Public),
			stackrecords.PropertySweepUploads:   strconv.FormatBool(sweeps),
			stackrecords.PropertyAllowedOrigins: strings.Join(declaredOrigins(in.Resource.Bucket), " "),
		},
	}
}

func (r Resource) Plan(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	spec, err := r.shapeStore(ctx, in)
	if err != nil {
		return "", "", err
	}
	action, reason, err := r.box.Containers.Plan(ctx, spec)
	if err != nil || action == provider.ActionCreate {
		return action, "", err
	}
	var change component.Change
	if action != provider.ActionKeep {
		change.Add(provider.ActionUpdate, "its object store "+spec.Name+" is replaced: "+reason)
	}
	root, err := r.storeRoot(ctx, in.Ref, spec.Name)
	if err != nil {
		return "", "", err
	}
	if root.secret == "" {
		change.Add(provider.ActionUpdate, "the box keeps no credential for its object store "+spec.Name+", so a new one is minted that the running store was never handed")
		return change.Report()
	}
	route := storeRoute(in.Ref, spec.Name)
	upstream, err := r.box.Routing.Serving(ctx, route.RouteKey)
	if err != nil {
		return "", "", err
	}
	if upstream != route.Upstream {
		change.Add(provider.ActionUpdate, "its object store "+spec.Name+" is routed to from "+route.Pointer)
	}
	sessions, reason, err := r.objects.planBucket(ctx, newSessionsBucketSpec(in.Ref, spec.Name, root.secret))
	if err != nil {
		return "", "", err
	}
	switch sessions {
	case provider.ActionKeep:
	case provider.ActionCreate:
		change.Add(provider.ActionUpdate, "its object store's upload sessions bucket is created")
	default:
		change.Add(provider.ActionUpdate, "its object store's upload sessions bucket changes: "+reason)
	}
	declared, err := r.readDeclaredBucketSpec(ctx, in, spec.Name, root.secret)
	if err != nil {
		return "", "", err
	}
	action, reason, err = r.objects.planBucket(ctx, declared)
	if err != nil {
		return "", "", err
	}
	change.Add(action, reason)
	change.AddRecorded(recorded, newBucketBinding(in, declared, false), stackrecords.PropertySweepUploads)
	return change.Report()
}

func (r Resource) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	spec, err := r.shapeStore(ctx, in)
	if err != nil {
		return provider.Binding{}, err
	}
	spec, err = r.stores.shaped(in.Resource.Name, spec)
	if err != nil {
		return provider.Binding{}, err
	}
	progress.Say("Provisioning bucket " + in.Resource.Name + " in object store " + spec.Name)

	sessionsExpire, started := false, false
	root, err := r.stores.once(spec.Name, func() (storeCredential, error) {
		credential, err := r.storeCredential(ctx, in.Ref, spec.Name)
		if err != nil {
			return storeCredential{}, err
		}
		if err := r.box.Containers.Run(ctx, spec, credential.secret); err != nil {
			return storeCredential{}, err
		}
		if err := r.box.Routing.RouteResource(ctx, storeRoute(in.Ref, spec.Name)); err != nil {
			return storeCredential{}, err
		}
		expires, err := r.objects.provisionBucket(ctx, newSessionsBucketSpec(in.Ref, spec.Name, credential.secret))
		if err != nil {
			return storeCredential{}, err
		}
		sessionsExpire, started = expires, true
		return credential, nil
	})
	if err != nil {
		return provider.Binding{}, err
	}
	if started {
		r.stores.expiring(sessionsExpire)
	}

	declared, err := r.readDeclaredBucketSpec(ctx, in, spec.Name, root.secret)
	if err != nil {
		return provider.Binding{}, err
	}
	expires, err := r.objects.provisionBucket(ctx, declared)
	if err != nil {
		return provider.Binding{}, err
	}
	r.stores.expiring(expires)
	return newBucketBinding(in, declared, r.stores.sweeping()), nil
}

func declaredPublic(spec *provider.BucketSpec) bool {
	return spec != nil && spec.Public
}

func declaredOrigins(spec *provider.BucketSpec) []string {
	if spec == nil {
		return nil
	}
	return spec.AllowedOrigins
}

func (r Resource) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	if err := r.dropBucket(ctx, ref, binding, progress); err != nil {
		return err
	}
	r.stores.forgot(ref.Name, binding.Name)
	return r.reconcileStore(ctx, ref, progress)
}

func (r Resource) dropBucket(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	store, err := StoreName(ref)
	if err != nil {
		return err
	}
	root, err := r.storeRoot(ctx, ref, store)
	if err != nil {
		return err
	}
	if root.secret == "" {
		progress.Say("Skipped removing bucket " + binding.Name + ": the box keeps no credential for object store " + store)
		return nil
	}
	bucket := binding.Properties[provider.PropertyBucket]
	if bucket == "" {
		if bucket, err = storeBucketName(ref, binding.Name); err != nil {
			return err
		}
	}
	progress.Say("Removing bucket " + binding.Name + " and its objects from object store " + store)
	return r.objects.removeBucket(ctx, storeBucketSpec(ref, store, root.secret, Spec{Bucket: bucket}))
}

func (r Resource) reconcileStore(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	last, err := r.lastBucket(ctx, ref)
	if err != nil || !last {
		return err
	}
	return r.removeStore(ctx, ref, progress)
}

func (r Resource) lastBucket(ctx context.Context, ref provider.StackRef) (bool, error) {
	entries, err := stackrecords.List(ctx, r.box.KeyValues, ref.Tier, ref.Project)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name.Env != ref.Name.Env {
			continue
		}
		for _, binding := range entry.Bindings {
			if binding.Type != provider.BindingBucket || r.stores.forgotten(entry.Name, binding.Name) {
				continue
			}
			return false, nil
		}
	}
	return true, nil
}

func (r Resource) removeStore(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	store, err := StoreName(ref)
	if err != nil {
		return err
	}
	progress.Say("Removing object store " + store + ": no bucket in this environment uses it any more")
	if err := r.box.Routing.UnrouteApp(ctx, storeRoute(ref, store).RouteKey); err != nil {
		return err
	}
	if err := r.box.Containers.Remove(ctx, container.Ref{
		Tier: ref.Tier, Project: ref.Project, Resource: storeResource, Name: store,
	}); err != nil {
		return err
	}
	accounts, err := r.storeAccounts(ctx, ref)
	if err != nil {
		return err
	}
	return r.box.Containers.ForgetKept(ctx, ref.Tier, accounts)
}

func (r Resource) storeAccounts(ctx context.Context, ref provider.StackRef) ([]string, error) {
	entries, err := stackrecords.List(ctx, r.box.KeyValues, ref.Tier, ref.Project)
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
		if key := AccountKey(ref.Project, env, app); !slices.Contains(accounts, key) {
			accounts = append(accounts, key)
		}
	}
	return accounts, nil
}

func (r Resource) storeRoot(ctx context.Context, ref provider.StackRef, store string) (storeCredential, error) {
	sealed, err := r.box.Containers.Kept(ctx, ref.Tier, store)
	if err != nil {
		return storeCredential{}, err
	}
	if len(sealed) == 0 {
		return storeCredential{}, nil
	}
	bound, err := NewSecretAssociatedData(ref)
	if err != nil {
		return storeCredential{}, err
	}
	opened, err := r.box.Cipher.Open(ctx, ref.Tier, bound, sealed)
	if err != nil {
		return storeCredential{}, err
	}
	return storeCredential{sealed: sealed, secret: string(opened)}, nil
}
