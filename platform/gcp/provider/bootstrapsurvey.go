package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const enabledVersion = "ENABLED"

const (
	stateApplying = "applying"
	stateRemoving = "removing"
	stateComplete = "complete"
)

type stamp struct {
	Schema int    `json:"schema"`
	State  string `json:"state"`
	Writer string `json:"writer"`
	Digest string `json:"digest"`
}

type survey struct {
	Class      providerkit.Class
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
	for _, held := range items {
		if !s.holds(held) || s.mends(held) != "" {
			return false
		}
	}
	return true
}

func (b bootstrapper) survey(ctx context.Context, class providerkit.Class) (survey, error) {
	read := survey{
		Class:    class,
		Project:  b.clients.project,
		Region:   b.clients.region,
		Emulated: b.clients.endpoint != "",
		standing: map[string]bool{},
		mending:  map[string]string{},
	}
	for _, held := range bootstrapItems(read.Project, class) {
		stands, err := b.stands(ctx, held)
		if err != nil {
			return survey{}, err
		}
		read.standing[held.ID()] = stands.held
		if stands.mends != "" {
			read.mending[held.ID()] = stands.mends
		}
	}

	sibling, err := b.stamped(ctx, BucketName(read.Project, siblingOf(class)))
	if err != nil {
		return survey{}, err
	}
	read.sibling = sibling.held

	own, err := b.stamped(ctx, BucketName(read.Project, class))
	if err != nil {
		return survey{}, err
	}
	read.Present, read.Stamp, read.Generation = own.held, own.stamp, own.generation

	if read.stateOf, err = b.stateHeldIn(ctx, StateBucketName(read.Project, class)); err != nil {
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
	default:
		return standing{}, providerkit.Refuse(providerkit.CodeInvalid, "gcp: nothing surveys a %s", held.Kind)
	}
}

func stood(held bool, err error) (standing, error) { return standing{held: held}, err }

func (b bootstrapper) databaseStands(ctx context.Context) (standing, error) {
	if b.clients.endpoint != "" {
		return standing{held: true}, nil
	}
	service, err := b.clients.Databases()
	if err != nil {
		return standing{}, err
	}
	held, err := attempted(ctx, service.Projects.Databases.Get(databasePath(b.clients.project)).Context(ctx).Do)
	if absent(err) {
		return standing{}, nil
	}
	if err != nil {
		return standing{}, fmt.Errorf("read the %q Firestore database: %w", recordDatabase, err)
	}
	if held.DeleteProtectionState != protectionOn {
		return standing{held: true, mends: reasonUnprotected}, nil
	}
	return standing{held: true}, nil
}

func databasePath(project string) string {
	return "projects/" + project + "/databases/" + recordDatabase
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
		return false, fmt.Errorf("read the %s key ring: %w", KeyRing, err)
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

func secretPath(project, name string) string {
	return "projects/" + project + "/secrets/" + name
}

func keyRingPath(c *clients) string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s", c.project, c.region, KeyRing)
}

func keyPath(c *clients, name string) string {
	return keyRingPath(c) + "/cryptoKeys/" + name
}
