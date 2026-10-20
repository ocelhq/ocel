package bucket

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	s3store "github.com/ocelhq/ocel/platform/s3"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

func (r Resource) ensureRoot(ctx context.Context, ref provider.StackRef) (*container.Spec, storeCredential, error) {
	name, err := StoreName(ref)
	if err != nil {
		return nil, storeCredential{}, err
	}
	container := r.stores.shape(name)
	if container == nil {
		shaped, err := storeContainer(resources.ProvisionRequest{Ref: storeRef(ref)})
		if err != nil {
			return nil, storeCredential{}, err
		}
		container = &shaped
	}
	root, err := r.stores.once(container.Name, func() (storeCredential, error) {
		return r.storeCredential(ctx, ref, container.Name)
	})
	return container, root, err
}

func (r Resource) OpenRoot(ctx context.Context, ref provider.StackRef) (s3store.Store, func(), error) {
	container, root, err := r.ensureRoot(ctx, ref)
	if err != nil {
		return s3store.Store{}, nil, err
	}
	local, stop, err := r.box.Apps.Forward(ctx, container.Name, storePort)
	if err != nil {
		return s3store.Store{}, nil, err
	}
	return s3store.Store{
		Endpoint:        "http://" + local,
		Region:          storeRegion,
		AccessKeyID:     storeAccessKey,
		SecretAccessKey: root.secret,
		PathStyle:       true,
	}, stop, nil
}

func (r Resource) GrantApp(ctx context.Context, spec provider.StackSpec) (*boxapi.Store, error) {
	if !slices.ContainsFunc(spec.App.Values.Bindings, func(binding provider.Binding) bool {
		return binding.Type == provider.BindingBucket && !binding.Endpointed()
	}) {
		return nil, nil
	}
	container, root, err := r.ensureRoot(ctx, spec.Ref)
	if err != nil {
		return nil, err
	}
	own, err := r.storeAccount(ctx, spec, container.Name, root)
	if err != nil {
		return nil, err
	}
	return &boxapi.Store{
		Env:          storeRef(spec.Ref).Name.String(),
		Endpoint:     "http://" + container.Name + ":" + storePort,
		Region:       storeRegion,
		AccessKeyID:  own.key,
		PathStyle:    true,
		Pointer:      storeRoute(spec.Ref, container.Name).Pointer,
		Volume:       container.VolumeName(),
		Sessions:     SessionsPrefix(spec.Ref, appNameOf(spec.App)),
		Granted:      grantedBuckets(spec.App),
		SweepUploads: sweptUploads(spec.App),
		Sealed:       base64.StdEncoding.EncodeToString(own.credential.sealed),
	}, nil
}

func SessionsPrefix(ref provider.StackRef, app string) string {
	return s3store.SessionsBucket() + "/" + storeRef(ref).Name.String() + "/" + naming.Sanitize(app)
}

type appAccount struct {
	key        string
	credential storeCredential
}

func (r Resource) storeAccount(ctx context.Context, spec provider.StackSpec, store string, root storeCredential) (appAccount, error) {
	env := storeRef(spec.Ref).Name.String()
	key := AccountKey(spec.Ref.Project, env, appNameOf(spec.App))
	credential, err := r.stores.once(key, func() (storeCredential, error) {
		return r.storeCredential(ctx, spec.Ref, key)
	})
	if err != nil {
		return appAccount{}, err
	}
	if err := r.objects.grantAccount(ctx, Account{
		Store:       store,
		Tier:        spec.Ref.Tier,
		Endpoint:    "http://127.0.0.1:" + storePort,
		Region:      storeRegion,
		RootKeyID:   storeAccessKey,
		RootSecret:  root.secret,
		AccessKeyID: key,
		SecretKey:   credential.secret,
		Buckets:     boundBuckets(spec.App),
		Sessions:    SessionsPrefix(spec.Ref, appNameOf(spec.App)),
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
				binding.Properties[stackrecords.PropertySweepUploads] == "true"
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

func (r Resource) RevokeApp(ctx context.Context, ref provider.StackRef, app string) error {
	key := AccountKey(ref.Project, storeRef(ref).Name.String(), app)
	sealed, err := r.box.Containers.Kept(ctx, ref.Tier, key)
	if err != nil || len(sealed) == 0 {
		return err
	}
	store, err := StoreName(ref)
	if err != nil {
		return err
	}
	root, err := r.storeRoot(ctx, ref, store)
	if err != nil {
		return err
	}
	if root.secret != "" {
		if err := r.objects.revokeAccount(ctx, Account{
			Store:       store,
			Tier:        ref.Tier,
			Endpoint:    "http://127.0.0.1:" + storePort,
			Region:      storeRegion,
			RootKeyID:   storeAccessKey,
			RootSecret:  root.secret,
			AccessKeyID: key,
		}); err != nil {
			return err
		}
	}
	return r.box.Containers.ForgetKept(ctx, ref.Tier, []string{key})
}
