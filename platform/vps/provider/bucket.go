package vps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
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

	storeSecretFolder = "resources"
	storeSecretName   = "rootkey"

	storeBucketNameMax = 63
)

const storeHealthPath = "/health/ready"

// StoreLabel is the label a box's edge serves a project's store under.
const StoreLabel = "storage"

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

type standingStores struct {
	mu    sync.Mutex
	stood map[string]string
}

func (s *standingStores) once(name string, stand func() (string, error)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if secret, held := s.stood[name]; held {
		return secret, nil
	}
	secret, err := stand()
	if err != nil {
		return "", err
	}
	if s.stood == nil {
		s.stood = map[string]string{}
	}
	s.stood[name] = secret
	return secret, nil
}

// Bucket stands this project's object store up, if it is not already standing,
// and binds the app to the bucket inside it that this resource names.
func (p *Provider) Bucket(ctx context.Context, in resources.Instruction, report providerkit.Reporter) (providerkit.Binding, error) {
	if external := p.options.Bucket; external.configured() {
		return p.externalBucket(in, report)
	}

	spec := storeContainer(in)
	spec, err := p.reshaped(ctx, in, transformTypeBucket, spec)
	if err != nil {
		return providerkit.Binding{}, err
	}
	if report != nil {
		report.Say("Standing bucket " + in.Resource.Name + " up in " + spec.Name)
	}

	secret, err := p.stores.once(spec.Name, func() (string, error) {
		held, err := p.heldSecret(ctx, in, spec.Name, storeSecretFolder, storeSecretName, mintStoreSecret)
		if err != nil {
			return "", err
		}
		if err := p.host.StandResource(ctx, spec, held); err != nil {
			return "", err
		}
		return held, nil
	})
	if err != nil {
		return providerkit.Binding{}, err
	}

	bucket := storeBucketName(in.Ref, in.Resource.Name)
	if err := p.host.ProvisionBucket(ctx, host.BucketSpec{
		Store:          spec.Name,
		Class:          in.Ref.Class,
		Endpoint:       "http://127.0.0.1:" + storePort,
		Region:         storeRegion,
		AccessKeyID:    storeAccessKey,
		SecretKey:      secret,
		Bucket:         bucket,
		AllowedOrigins: declaredOrigins(in.Resource.Bucket),
		Public:         declaredPublic(in.Resource.Bucket),
	}); err != nil {
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
