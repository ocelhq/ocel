package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const enabledVersion = "ENABLED"

type state string

const (
	stateApplying state = "applying"
	stateRemoving state = "removing"
	stateComplete state = "complete"
)

type stamp struct {
	Schema   int      `json:"schema"`
	State    state    `json:"state"`
	Writer   string   `json:"writer"`
	Digest   string   `json:"digest"`
	Features []string `json:"features,omitempty"`
}

type survey struct {
	Class      edge.Class
	Names      Names
	Project    string
	Region     string
	Present    bool
	Stamp      stamp
	Generation int64
	Emulated   bool

	present map[string]bool
	mending map[string]string
	sibling bool
	stateOf string
}

func (s survey) has(target item) bool { return s.present[target.ID()] }

func (s survey) mends(target item) string { return s.mending[target.ID()] }

func (s survey) current(items []item) bool {
	for _, item := range items {
		if !s.has(item) || s.mends(item) != "" {
			return false
		}
	}
	return true
}

func (b bootstrap) survey(ctx context.Context, class edge.Class) (survey, error) {
	read := survey{
		Class:    class,
		Names:    b.clients.Names,
		Project:  b.clients.project,
		Region:   b.clients.region,
		Emulated: b.clients.emulated(),
		present:  map[string]bool{},
		mending:  map[string]string{},
	}
	for _, item := range bootstrapItems(read.Names, class, read.Emulated) {
		found, err := b.presenceOf(ctx, class, item)
		if err != nil {
			return survey{}, err
		}
		read.present[item.ID()] = found.present
		if found.mends != "" {
			read.mending[item.ID()] = found.mends
		}
	}

	sibling, err := b.stamped(ctx, read.Names.Bucket(siblingOf(class)))
	if err != nil {
		return survey{}, err
	}
	read.sibling = sibling.present

	own, err := b.stamped(ctx, read.Names.Bucket(class))
	if err != nil {
		return survey{}, err
	}
	read.Present, read.Stamp, read.Generation = own.present, own.stamp, own.generation

	if read.stateOf, err = b.stateIn(ctx, read.Names.StateBucket(class)); err != nil {
		return survey{}, err
	}
	return read, nil
}

type stamped struct {
	present    bool
	stamp      stamp
	generation int64
}

func (b bootstrap) stamped(ctx context.Context, bucket string) (stamped, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return stamped{}, err
	}
	reader, err := client.Bucket(bucket).Object(StampObject).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) || errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
		return stamped{}, nil
	}
	if err != nil {
		return stamped{}, fmt.Errorf("read %s in %s: %w", StampObject, bucket, err)
	}
	defer reader.Close()
	written, err := io.ReadAll(reader)
	if err != nil {
		return stamped{}, fmt.Errorf("read %s in %s: %w", StampObject, bucket, err)
	}
	var read stamp
	if err := json.Unmarshal(written, &read); err != nil {
		return stamped{}, fmt.Errorf("read %s in %s: %w", StampObject, bucket, err)
	}
	return stamped{present: true, stamp: read, generation: reader.Attrs.Generation}, nil
}

const stateRoot = ".pulumi/stacks/"

func (b bootstrap) stateIn(ctx context.Context, bucket string) (string, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return "", err
	}
	attrs, err := client.Bucket(bucket).Objects(ctx, nil).Next()
	if errors.Is(err, iterator.Done) || errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list what %s contains: %w", bucket, err)
	}
	return stackNamed(attrs.Name), nil
}

func stackNamed(object string) string {
	name := strings.TrimPrefix(object, stateRoot)
	name = strings.TrimSuffix(name, ".json")
	if name == "" {
		return object
	}
	return name
}

type presence struct {
	present bool
	mends   string
}

func (b bootstrap) presenceOf(ctx context.Context, class edge.Class, repository item) (presence, error) {
	switch repository.Kind {
	case KindDatabase:
		return b.databasePresence(ctx)
	case KindBucket:
		return b.bucketPresence(ctx, repository.Name)
	case KindKeyRing:
		return presenceFrom(b.keyRingExists(ctx))
	case KindKey:
		return presenceFrom(b.keyUsable(ctx, repository.Name))
	case KindSecret:
		return presenceFrom(b.secretExists(ctx, repository.Name))
	case KindRepository:
		return b.repositoryPresence(ctx, repository.Name)
	case KindServiceAccount:
		return b.accountPresence(ctx, class, repository.Name)
	}
	return presence{}, refusal.Refuse(refusal.CodeInvalid, "gcp: nothing surveys a %s", repository.Kind)
}

func presenceFrom(present bool, err error) (presence, error) { return presence{present: present}, err }

func (b bootstrap) databasePresence(ctx context.Context) (presence, error) {
	if b.clients.emulated() {
		return presence{present: true}, nil
	}
	service, err := b.clients.Databases()
	if err != nil {
		return presence{}, err
	}
	database, err := attempted(ctx, service.Projects.Databases.Get(databasePath(b.clients)).Context(ctx).Do)
	if absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %q Firestore database: %w", b.clients.Database(), err)
	}
	if database.DeleteProtectionState != protectionOn {
		return presence{present: true, mends: reasonUnprotected}, nil
	}
	return presence{present: true}, nil
}

func databasePath(c *clients) string {
	return "projects/" + c.project + "/databases/" + c.Database()
}

func (b bootstrap) bucketPresence(ctx context.Context, name string) (presence, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return presence{}, err
	}
	attrs, err := client.Bucket(name).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
			return presence{}, nil
		}
		return presence{}, fmt.Errorf("read the %s bucket: %w", name, err)
	}
	return bucketPresenceOf(attrs, b.clients.emulated()), nil
}

func bucketPresenceOf(attrs *storage.BucketAttrs, emulated bool) presence {
	if !emulated && !locked(attrs) {
		return presence{present: true, mends: reasonUnlocked}
	}
	return presence{present: true}
}

func (b bootstrap) keyRingExists(ctx context.Context) (bool, error) {
	client, err := b.clients.KMS()
	if err != nil {
		return false, err
	}
	if _, err := dialled(ctx, func() (*kmspb.KeyRing, error) {
		return client.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: keyRingPath(b.clients)})
	}); err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("read the %s key ring: %w", b.clients.KeyRing(), err)
	}
	return true, nil
}

func (b bootstrap) keyUsable(ctx context.Context, name string) (bool, error) {
	key, err := b.readKey(ctx, name)
	if err != nil {
		return false, err
	}
	return usable(key.GetPrimary()), nil
}

func (b bootstrap) readKey(ctx context.Context, name string) (*kmspb.CryptoKey, error) {
	client, err := b.clients.KMS()
	if err != nil {
		return nil, err
	}
	key, err := dialled(ctx, func() (*kmspb.CryptoKey, error) {
		return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: keyPath(b.clients, name)})
	})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s key: %w", name, err)
	}
	return key, nil
}

func usable(version *kmspb.CryptoKeyVersion) bool {
	switch version.GetState() {
	case kmspb.CryptoKeyVersion_DESTROY_SCHEDULED, kmspb.CryptoKeyVersion_DESTROYED:
		return false
	default:
		return version != nil
	}
}

func (b bootstrap) secretExists(ctx context.Context, name string) (bool, error) {
	service, err := b.clients.Secrets()
	if err != nil {
		return false, err
	}
	_, err = attempted(ctx, service.Projects.Secrets.Get(secretPath(b.clients.project, name)).Context(ctx).Do)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the %s secret: %w", name, err)
	}
	return b.passphraseExists(ctx, name)
}

func (b bootstrap) passphraseExists(ctx context.Context, name string) (bool, error) {
	service, err := b.clients.Secrets()
	if err != nil {
		return false, err
	}
	version, err := attempted(ctx, service.Projects.Secrets.Versions.Get(
		secretPath(b.clients.project, name)+"/versions/latest").Context(ctx).Do)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read whether the %s secret has a passphrase: %w", name, err)
	}
	return version.State == enabledVersion, nil
}

func (b bootstrap) repositoryPresence(ctx context.Context, name string) (presence, error) {
	service, err := b.clients.Repositories()
	if err != nil {
		return presence{}, err
	}
	repository, err := attempted(ctx, service.Projects.Locations.Repositories.Get(repositoryPath(b.clients, name)).Context(ctx).Do)
	if absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %s image repository: %w", name, err)
	}
	return repositoryPresenceOf(name, repository)
}

func repositoryPresenceOf(name string, repository *artifactregistry.Repository) (presence, error) {
	if repository.Format != dockerImages {
		return presence{}, refusal.Refuse(refusal.CodeInvalid,
			"the %s repository already exists and stores %s packages, and Artifact Registry never changes the format of one: "+
				"Cloud Run runs %s images and nothing this bootstrap does would make it store them.\n"+
				"Delete that repository, or bootstrap under a namespace naming another in %s",
			name, repository.Format, dockerImages, provider.NamespaceEnvVar)
	}
	if !pruned(repository.CleanupPolicies) {
		return presence{present: true, mends: reasonUnpruned}, nil
	}
	return presence{present: true}, nil
}

func pruned(policies map[string]artifactregistry.CleanupPolicy) bool {
	if len(policies) != len(pruningUntaggedImages()) {
		return false
	}
	policy, named := policies[dropUntaggedPolicy]
	return named && policy.Action == deleteImages && policy.Condition != nil &&
		policy.Condition.TagState == untaggedImages && policy.Condition.OlderThan == untaggedLifetime
}

func (b bootstrap) accountPresence(ctx context.Context, class edge.Class, name string) (presence, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return presence{}, err
	}
	_, err = attempted(ctx, service.Projects.ServiceAccounts.Get(accountPath(b.clients, name)).Context(ctx).Do)
	if absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %s service account: %w", name, err)
	}
	policy, err := b.accountPolicy(ctx, name)
	if err != nil {
		return presence{}, err
	}
	member, err := b.clients.Principal(ctx)
	if err != nil {
		return presence{}, err
	}
	if !granted(policy, memberOf(member)) {
		return presence{present: true, mends: reasonUngranted}, nil
	}
	reads, err := b.readsGranted(ctx, class)
	if err != nil {
		return presence{}, err
	}
	if !reads {
		return presence{present: true, mends: reasonUnread}, nil
	}
	return presence{present: true}, nil
}

func (b bootstrap) accountPolicy(ctx context.Context, name string) (*iam.Policy, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return nil, err
	}
	policy, err := attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(accountPath(b.clients, name)).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("read who may run apps as the %s service account: %w", name, err)
	}
	return policy, nil
}

func granted(policy *iam.Policy, member string) bool {
	for _, binding := range policy.Bindings {
		if binding.Role == runAsRole && slices.Contains(binding.Members, member) {
			return true
		}
	}
	return false
}

func memberOf(principal string) string {
	if strings.HasSuffix(principal, accountDomain) {
		return "serviceAccount:" + principal
	}
	return "user:" + principal
}

func accountPath(c *clients, name string) string {
	return "projects/" + c.project + "/serviceAccounts/" + name + "@" + c.project + accountDomain
}

func repositoryParent(c *clients) string {
	return fmt.Sprintf("projects/%s/locations/%s", c.project, c.region)
}

func repositoryPath(c *clients, name string) string {
	return repositoryParent(c) + "/repositories/" + name
}

func secretPath(project, name string) string {
	return "projects/" + project + "/secrets/" + name
}

func keyRingPath(c *clients) string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s", c.project, c.region, c.KeyRing())
}

func keyPath(c *clients, name string) string {
	return keyRingPath(c) + "/cryptoKeys/" + name
}
