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

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const enabledVersion = "ENABLED"

type state string

const (
	stateApplying state = "applying"
	stateRemoving state = "removing"
	stateComplete state = "complete"
)

type stamp struct {
	Schema int    `json:"schema"`
	State  state  `json:"state"`
	Writer string `json:"writer"`
	Digest string `json:"digest"`
}

type survey struct {
	Class      providerkit.Class
	Names      Names
	Project    string
	Region     string
	Present    bool
	Stamp      stamp
	Generation int64
	Emulated   bool

	standing map[string]bool
	mending  map[string]string
	sibling  bool
	stateOf  string
}

func (s survey) holds(held item) bool { return s.standing[held.ID()] }

func (s survey) mends(held item) string { return s.mending[held.ID()] }

func (s survey) current(items []item) bool {
	for _, item := range items {
		if !s.holds(item) || s.mends(item) != "" {
			return false
		}
	}
	return true
}

func (b bootstrapper) survey(ctx context.Context, class providerkit.Class) (survey, error) {
	read := survey{
		Class:    class,
		Names:    b.clients.Names,
		Project:  b.clients.project,
		Region:   b.clients.region,
		Emulated: b.clients.emulated(),
		standing: map[string]bool{},
		mending:  map[string]string{},
	}
	for _, item := range bootstrapItems(read.Names, class, read.Emulated) {
		stands, err := b.stands(ctx, item)
		if err != nil {
			return survey{}, err
		}
		read.standing[item.ID()] = stands.held
		if stands.mends != "" {
			read.mending[item.ID()] = stands.mends
		}
	}

	sibling, err := b.stamped(ctx, read.Names.Bucket(siblingOf(class)))
	if err != nil {
		return survey{}, err
	}
	read.sibling = sibling.held

	own, err := b.stamped(ctx, read.Names.Bucket(class))
	if err != nil {
		return survey{}, err
	}
	read.Present, read.Stamp, read.Generation = own.held, own.stamp, own.generation

	if read.stateOf, err = b.stateHeldIn(ctx, read.Names.StateBucket(class)); err != nil {
		return survey{}, err
	}
	return read, nil
}

type stamped struct {
	held       bool
	stamp      stamp
	generation int64
}

func (b bootstrapper) stamped(ctx context.Context, bucket string) (stamped, error) {
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
	return stamped{held: true, stamp: read, generation: reader.Attrs.Generation}, nil
}

const stateRoot = ".pulumi/stacks/"

func (b bootstrapper) stateHeldIn(ctx context.Context, bucket string) (string, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return "", err
	}
	attrs, err := client.Bucket(bucket).Objects(ctx, nil).Next()
	if errors.Is(err, iterator.Done) || errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list what %s holds: %w", bucket, err)
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

type standing struct {
	held  bool
	mends string
}

func (b bootstrapper) stands(ctx context.Context, held item) (standing, error) {
	switch held.Kind {
	case KindDatabase:
		return b.databaseStands(ctx)
	case KindBucket:
		return stood(b.bucketStands(ctx, held.Name))
	case KindKeyRing:
		return stood(b.keyRingStands(ctx))
	case KindKey:
		return stood(b.keyStands(ctx, held.Name))
	case KindSecret:
		return stood(b.secretStands(ctx, held.Name))
	case KindRepository:
		return b.repositoryStands(ctx, held.Name)
	case KindServiceAccount:
		return b.accountStands(ctx, held.Name)
	}
	return standing{}, providerkit.Refuse(providerkit.CodeInvalid, "gcp: nothing surveys a %s", held.Kind)
}

func stood(held bool, err error) (standing, error) { return standing{held: held}, err }

func (b bootstrapper) databaseStands(ctx context.Context) (standing, error) {
	if b.clients.emulated() {
		return standing{held: true}, nil
	}
	service, err := b.clients.Databases()
	if err != nil {
		return standing{}, err
	}
	held, err := attempted(ctx, service.Projects.Databases.Get(databasePath(b.clients)).Context(ctx).Do)
	if absent(err) {
		return standing{}, nil
	}
	if err != nil {
		return standing{}, fmt.Errorf("read the %q Firestore database: %w", b.clients.Database(), err)
	}
	if held.DeleteProtectionState != protectionOn {
		return standing{held: true, mends: reasonUnprotected}, nil
	}
	return standing{held: true}, nil
}

func databasePath(c *clients) string {
	return "projects/" + c.project + "/databases/" + c.Database()
}

func (b bootstrapper) bucketStands(ctx context.Context, name string) (bool, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return false, err
	}
	if _, err := client.Bucket(name).Attrs(ctx); err != nil {
		if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
			return false, nil
		}
		return false, fmt.Errorf("read the %s bucket: %w", name, err)
	}
	return true, nil
}

func (b bootstrapper) keyRingStands(ctx context.Context) (bool, error) {
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

func (b bootstrapper) keyStands(ctx context.Context, name string) (bool, error) {
	key, err := b.keyHeld(ctx, name)
	if err != nil {
		return false, err
	}
	return usable(key.GetPrimary()), nil
}

func (b bootstrapper) keyHeld(ctx context.Context, name string) (*kmspb.CryptoKey, error) {
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

func (b bootstrapper) secretStands(ctx context.Context, name string) (bool, error) {
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
	return b.passphraseHeld(ctx, name)
}

func (b bootstrapper) passphraseHeld(ctx context.Context, name string) (bool, error) {
	service, err := b.clients.Secrets()
	if err != nil {
		return false, err
	}
	held, err := attempted(ctx, service.Projects.Secrets.Versions.Get(
		secretPath(b.clients.project, name)+"/versions/latest").Context(ctx).Do)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read whether the %s secret holds a passphrase: %w", name, err)
	}
	return held.State == enabledVersion, nil
}

func (b bootstrapper) repositoryStands(ctx context.Context, name string) (standing, error) {
	service, err := b.clients.Repositories()
	if err != nil {
		return standing{}, err
	}
	held, err := attempted(ctx, service.Projects.Locations.Repositories.Get(repositoryPath(b.clients, name)).Context(ctx).Do)
	if absent(err) {
		return standing{}, nil
	}
	if err != nil {
		return standing{}, fmt.Errorf("read the %s image repository: %w", name, err)
	}
	return repositoryStanding(name, held)
}

func repositoryStanding(name string, held *artifactregistry.Repository) (standing, error) {
	if held.Format != dockerImages {
		return standing{}, providerkit.Refuse(providerkit.CodeInvalid,
			"the %s repository already stands and holds %s packages, and Artifact Registry never changes the format of one: "+
				"Cloud Run runs %s images and nothing this bootstrap does would make it hold them.\n"+
				"Delete that repository, or bootstrap under a namespace naming another in %s",
			name, held.Format, dockerImages, providerkit.NamespaceEnvVar)
	}
	if !pruned(held.CleanupPolicies) {
		return standing{held: true, mends: reasonUnpruned}, nil
	}
	return standing{held: true}, nil
}

func pruned(policies map[string]artifactregistry.CleanupPolicy) bool {
	if len(policies) != len(pruningUntaggedImages()) {
		return false
	}
	policy, named := policies[dropUntaggedPolicy]
	return named && policy.Action == deleteImages && policy.Condition != nil &&
		policy.Condition.TagState == untaggedImages && policy.Condition.OlderThan == untaggedLifetime
}

func (b bootstrapper) accountStands(ctx context.Context, name string) (standing, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return standing{}, err
	}
	_, err = attempted(ctx, service.Projects.ServiceAccounts.Get(accountPath(b.clients, name)).Context(ctx).Do)
	if absent(err) {
		return standing{}, nil
	}
	if err != nil {
		return standing{}, fmt.Errorf("read the %s service account: %w", name, err)
	}
	policy, err := b.accountPolicy(ctx, name)
	if err != nil {
		return standing{}, err
	}
	member, err := b.clients.Principal(ctx)
	if err != nil {
		return standing{}, err
	}
	if !granted(policy, memberOf(member)) {
		return standing{held: true, mends: reasonUngranted}, nil
	}
	return standing{held: true}, nil
}

func (b bootstrapper) accountPolicy(ctx context.Context, name string) (*iam.Policy, error) {
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
