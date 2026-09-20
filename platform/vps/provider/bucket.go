package vps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
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
	storeSecretLen = 24

	storeBucketNameMax = 63
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

func storeName(ref providerkit.StackRef) string {
	return host.ResourceName(ref.Name.String(), storeResource, storeKind)
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
	mu    sync.Mutex
	stood map[string]storeCredential
	spec  *host.ResourceContainer
}

func (s *standingStores) shaped(spec host.ResourceContainer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spec = &spec
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
		Project: ref.Project, Class: ref.Class, Env: ref.Name.String(),
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
			"what this box keeps for %s opens to nothing, so there is no credential to reach the store with. Remove %s on the box and run this again",
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
	p.stores.shaped(spec)
	if report != nil {
		report.Say("Standing bucket " + in.Resource.Name + " up in " + spec.Name)
	}

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
		if err := p.host.ProvisionBucket(ctx, storeBucketSpec(in.Ref, spec.Name, held.secret,
			host.BucketSpec{Bucket: constants.StoreSessionsBucket()})); err != nil {
			return storeCredential{}, err
		}
		return held, nil
	})
	if err != nil {
		return providerkit.Binding{}, err
	}

	bucket := storeBucketName(in.Ref, in.Resource.Name)
	if err := p.host.ProvisionBucket(ctx, storeBucketSpec(in.Ref, spec.Name, held.secret, host.BucketSpec{
		Bucket:         bucket,
		AllowedOrigins: declaredOrigins(in.Resource.Bucket),
		Public:         declaredPublic(in.Resource.Bucket),
	})); err != nil {
		return providerkit.Binding{}, err
	}

	return providerkit.Binding{
		Type: providerkit.BindingBucket,
		Name: in.Resource.Name,
		Properties: map[string]string{
			providerkit.PropertyBucket: bucket,
		},
	}, nil
}

func (p *Provider) externalBucket(in resources.Instruction, report providerkit.Reporter) (providerkit.Binding, error) {
	if declaredPublic(in.Resource.Bucket) {
		return providerkit.Binding{}, providerkit.Refuse(providerkit.CodeInvalid,
			"bucket %s asks to be public and this project keeps its objects in the store named by option %q, whose anonymous access is that store's to grant and not ocel's: serve the objects through your app or a signed url, or drop `public` from %s",
			in.Resource.Name, "bucket", in.Resource.Name)
	}
	prefix := naming.Sanitize(in.Ref.Project) + "/" + naming.Sanitize(in.Ref.Name.Env) + "/" + naming.Sanitize(in.Resource.Name)
	if report != nil {
		report.Say("Binding bucket " + in.Resource.Name + " to " + p.options.Bucket.Bucket + "/" + prefix)
	}
	return providerkit.Binding{
		Type: providerkit.BindingBucket,
		Name: in.Resource.Name,
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
			Env:           plan.Ref.Name.String(),
			Endpoint:      external.Endpoint,
			PublicBaseURL: external.Endpoint,
			Region:        external.Region,
			AccessKeyID:   external.AccessKeyID,
			PathStyle:     external.PathStyle,
			Sessions:      external.Bucket + "/" + naming.Sanitize(plan.Ref.Project) + "/" + naming.Sanitize(plan.Ref.Name.Env),
			Sealed:        base64.StdEncoding.EncodeToString(sealed),
		}, nil
	}
	spec := p.stores.shape()
	if spec == nil {
		shaped := storeContainer(resources.Instruction{Ref: plan.Ref})
		spec = &shaped
	}
	held, err := p.stores.once(spec.Name, func() (storeCredential, error) {
		return p.storeCredential(ctx, plan.Ref, spec.Name)
	})
	if err != nil {
		return nil, err
	}
	return &live.Store{
		Env:         plan.Ref.Name.String(),
		Endpoint:    "http://" + spec.Name + ":" + storePort,
		Region:      storeRegion,
		AccessKeyID: storeAccessKey,
		PathStyle:   true,
		Pointer:     storeRoute(plan.Ref, spec.Name).Pointer,
		Volume:      spec.VolumeName(),
		Sessions:    constants.StoreSessionsBucket(),
		Sealed:      base64.StdEncoding.EncodeToString(held.sealed),
	}, nil
}

func declaredOrigins(spec *providerkit.BucketSpec) []string {
	if spec == nil {
		return nil
	}
	return spec.AllowedOrigins
}

func declaredPublic(spec *providerkit.BucketSpec) bool {
	return spec != nil && spec.Public
}

func (p *Provider) removeBucket(ctx context.Context, ref providerkit.StackRef, binding providerkit.Binding, report providerkit.Reporter) error {
	if external := p.options.Bucket; external.configured() {
		if report != nil {
			report.Say("Leaving bucket " + binding.Name + "'s objects under " +
				binding.Properties[providerkit.PropertyBucket] + " in the store this project was pointed at")
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
		Class:   ref.Class,
		Project: ref.Project,
		Store:   storeName(ref),
		Bucket:  bucket,
	})
}

var _ resources.Bucket = (*Provider)(nil)
