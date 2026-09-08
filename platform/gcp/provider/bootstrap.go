package gcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	reasonStanding = "already current"
	reasonEmulated = "emulator serves one implicit database"
	reasonRingKept = "Google never deletes a key ring, so this one outlives every bootstrap that used it"
	reasonShared   = "the %s bootstrap still stands and shares it"
	reasonDestroy  = "scheduled, destroyed after 24h"

	reasonUnprotected = "it stands with delete protection off, and one call would take every record both classes hold with it"
)

const passphraseBytes = 32

const (
	nativeFirestore = "FIRESTORE_NATIVE"
	protectionOn    = "DELETE_PROTECTION_ENABLED"
	protectionOff   = "DELETE_PROTECTION_DISABLED"
)

type bootstrapper struct {
	clients *clients
}

func (bootstrapper) Catalogue() []providerkit.Feature { return nil }

func (b bootstrapper) Describe(ctx context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	read, err := b.survey(ctx, class)
	if err != nil {
		return providerkit.Bootstrap{}, err
	}
	return described(read), nil
}

func described(read survey) providerkit.Bootstrap {
	items := bootstrapItems(read.Project, read.Class)
	return providerkit.Bootstrap{
		Class:      read.Class,
		Present:    read.Present,
		Unfinished: read.Present && read.Stamp.State != stateComplete,
		Held:       read,
		Stacks: []providerkit.BootstrapStack{{
			Name:          read.Project + "/" + string(read.Class),
			Present:       read.Present,
			Schema:        uint32(read.Stamp.Schema),
			DigestCurrent: read.Stamp.Digest == digestOf(items) && read.current(items),
			Writer:        read.Stamp.Writer,
		}},
	}
}

func (b bootstrapper) held(ctx context.Context, req providerkit.BootstrapRequest) (survey, error) {
	if carried, held := req.Held.(survey); held && carried.Class == req.Class {
		return carried, nil
	}
	return b.survey(ctx, req.Class)
}

func (b bootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.Plan, error) {
	read, err := b.held(ctx, req)
	if err != nil {
		return providerkit.Plan{}, err
	}
	if err := b.preflight(ctx, read); err != nil {
		return providerkit.Plan{}, err
	}
	groups := providerkit.DeriveGroups(described(read), nil, req)
	groups[0].Changes = planned(read, stackItems(read.Project, read.Class))

	params := providerkit.ChangeGroup{
		Kind:    providerkit.ParameterGroupKind,
		Name:    providerkit.ParameterGroupKind,
		Changes: planned(read, parameterItems(read.Class)),
	}
	params.Action, params.Reason = providerkit.RollUp(params.Changes)
	return providerkit.Plan{Groups: providerkit.Vendored(Vendor, append(groups, params))}, nil
}

func planned(read survey, items []item) []providerkit.Change {
	changes := make([]providerkit.Change, 0, len(items))
	for _, held := range items {
		change := providerkit.Change{
			Kind:   held.Kind,
			Name:   held.Name,
			Action: providerkit.ActionCreate,
			Reason: held.Note,
			Slow:   held.Slow,
		}
		switch {
		case read.Emulated && held.Kind == KindDatabase:
			change.Action, change.Reason, change.Slow = providerkit.ActionKeep, reasonEmulated, false
		case read.holds(held) && read.mends(held) != "":
			change.Action, change.Reason = providerkit.ActionUpdate, read.mends(held)
		case read.holds(held):
			change.Action, change.Reason, change.Slow = providerkit.ActionKeep, reasonStanding, false
		}
		changes = append(changes, change)
	}
	return changes
}

func (b bootstrapper) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	read, err := b.held(ctx, req)
	if err != nil {
		return err
	}
	if err := b.preflight(ctx, read); err != nil {
		return err
	}

	items := bootstrapItems(read.Project, req.Class)
	holder := item{Kind: KindBucket, Name: BucketName(read.Project, req.Class)}
	if err := b.stand(ctx, read, stampHolder(items, holder), report); err != nil {
		return err
	}

	written := stamp{
		Schema: providerkit.BootstrapSchema,
		State:  stateApplying,
		Writer: req.Writer.String(),
		Digest: digestOf(items),
	}
	generation, err := b.stampWith(ctx, read, written, read.Generation)
	if err != nil {
		return err
	}
	for _, held := range items {
		if held.ID() == holder.ID() {
			continue
		}
		if err := b.stand(ctx, read, held, report); err != nil {
			return err
		}
	}
	written.State = stateComplete
	_, err = b.stampWith(ctx, read, written, generation)
	return err
}

func stampHolder(items []item, holder item) item {
	if at := slices.IndexFunc(items, func(held item) bool { return held.ID() == holder.ID() }); at >= 0 {
		return items[at]
	}
	return holder
}

func (b bootstrapper) stand(ctx context.Context, read survey, held item, report providerkit.Reporter) error {
	emulated := read.Emulated && held.Kind == KindDatabase
	if mends := read.mends(held); mends != "" && !emulated {
		if err := b.mend(ctx, read, held); err != nil {
			return err
		}
		say(report, "mended "+held.ID()+": "+mends)
		return nil
	}
	if read.holds(held) || emulated {
		say(report, held.ID()+": "+reasonStanding)
		return nil
	}
	if err := b.make(ctx, read, held); err != nil {
		return err
	}
	say(report, "created "+held.ID())
	return nil
}

func (b bootstrapper) mend(ctx context.Context, read survey, held item) error {
	if held.Kind == KindDatabase {
		return b.protectDatabase(ctx)
	}
	return b.make(ctx, read, held)
}

func (b bootstrapper) make(ctx context.Context, read survey, held item) error {
	switch held.Kind {
	case KindDatabase:
		return b.makeDatabase(ctx, read)
	case KindBucket:
		return b.makeBucket(ctx, read, held)
	case KindKeyRing:
		return b.makeKeyRing(ctx)
	case KindKey:
		return b.makeKey(ctx, held.Name)
	case KindSecret:
		return b.makeSecret(ctx, held.Name)
	default:
		return providerkit.Refuse(providerkit.CodeInvalid, "gcp: nothing stands up a %s", held.Kind)
	}
}

func (b bootstrapper) stampWith(ctx context.Context, read survey, written stamp, generation int64) (int64, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return 0, err
	}
	body, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("write %s: %w", StampObject, err)
	}
	object := client.Bucket(BucketName(read.Project, read.Class)).Object(StampObject).If(onlyWriter(generation))
	writer := object.NewWriter(ctx)
	if _, err := writer.Write(append(body, '\n')); err != nil {
		writer.Close()
		return 0, b.stampRefusal(ctx, read, err)
	}
	if err := writer.Close(); err != nil {
		return 0, b.stampRefusal(ctx, read, err)
	}
	return writer.Attrs().Generation, nil
}

func onlyWriter(generation int64) storage.Conditions {
	if generation == 0 {
		return storage.Conditions{DoesNotExist: true}
	}
	return storage.Conditions{GenerationMatch: generation}
}

func (b bootstrapper) stampRefusal(ctx context.Context, read survey, err error) error {
	if answeredCode(err) != http.StatusPreconditionFailed {
		return fmt.Errorf("write %s: %w", StampObject, err)
	}
	read.Stamp.Writer = b.writerNow(ctx, read)
	return providerkit.Refuse(providerkit.CodeBusy,
		"%s wrote %s in %s while this run was writing it, and two bootstraps of the same class interleaving leave a stack neither of them describes.\n"+
			"Wait for that run to finish, then try again",
		writerNamed(read.Stamp.Writer), StampObject, BucketName(read.Project, read.Class))
}

func (b bootstrapper) writerNow(ctx context.Context, read survey) string {
	now, err := b.stamped(ctx, BucketName(read.Project, read.Class))
	if err != nil || !now.held {
		return read.Stamp.Writer
	}
	return now.stamp.Writer
}

func writerNamed(writer string) string {
	if writer == "" {
		return "another run"
	}
	return writer
}

func (b bootstrapper) makeBucket(ctx context.Context, read survey, held item) error {
	client, err := b.clients.Storage()
	if err != nil {
		return err
	}
	attrs := &storage.BucketAttrs{Location: read.Region, VersioningEnabled: held.Versioned}
	if err := client.Bucket(held.Name).Create(ctx, read.Project, attrs); err != nil && !taken(err) {
		return fmt.Errorf("create the %s bucket: %w", held.Name, err)
	}
	return nil
}

func taken(err error) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == http.StatusConflict
}

func (b bootstrapper) makeKeyRing(ctx context.Context) error {
	client, err := b.clients.KMS()
	if err != nil {
		return err
	}
	_, err = dialled(ctx, func() (*kmspb.KeyRing, error) {
		return client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
			Parent:    fmt.Sprintf("projects/%s/locations/%s", b.clients.project, b.clients.region),
			KeyRingId: KeyRing,
			KeyRing:   &kmspb.KeyRing{},
		})
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("create the %s key ring: %w", KeyRing, err)
	}
	return nil
}

func (b bootstrapper) makeKey(ctx context.Context, name string) error {
	client, err := b.clients.KMS()
	if err != nil {
		return err
	}
	_, err = dialled(ctx, func() (*kmspb.CryptoKey, error) {
		return client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
			Parent:      keyRingPath(b.clients),
			CryptoKeyId: name,
			CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
		})
	})
	if status.Code(err) != codes.AlreadyExists {
		if err != nil {
			return fmt.Errorf("create the %s key: %w", name, err)
		}
		return nil
	}
	minted, err := dialled(ctx, func() (*kmspb.CryptoKeyVersion, error) {
		return client.CreateCryptoKeyVersion(ctx, &kmspb.CreateCryptoKeyVersionRequest{
			Parent:           keyPath(b.clients, name),
			CryptoKeyVersion: &kmspb.CryptoKeyVersion{},
		})
	})
	if err != nil {
		return fmt.Errorf("give the %s key a version to seal under again: %w", name, err)
	}
	if _, err := until(ctx, fmt.Sprintf("the material the %s key seals under", name),
		func() (*kmspb.CryptoKeyVersion, error) {
			return dialled(ctx, func() (*kmspb.CryptoKeyVersion, error) {
				return client.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: minted.GetName()})
			})
		},
		func(version *kmspb.CryptoKeyVersion) bool {
			return version.GetState() == kmspb.CryptoKeyVersion_ENABLED
		}); err != nil {
		return err
	}
	if _, err := dialled(ctx, func() (*kmspb.CryptoKey, error) {
		return client.UpdateCryptoKeyPrimaryVersion(ctx, &kmspb.UpdateCryptoKeyPrimaryVersionRequest{
			Name:               keyPath(b.clients, name),
			CryptoKeyVersionId: path.Base(minted.GetName()),
		})
	}); err != nil {
		return fmt.Errorf("point the %s key at the version it seals under now: %w", name, err)
	}
	return nil
}

func (b bootstrapper) makeSecret(ctx context.Context, name string) error {
	service, err := b.clients.Secrets()
	if err != nil {
		return err
	}
	parent := "projects/" + b.clients.project
	_, err = attempted(ctx, service.Projects.Secrets.Create(parent, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(name).Context(ctx).Do)
	if err != nil && answeredCode(err) != http.StatusConflict {
		return fmt.Errorf("create the %s secret: %w", name, err)
	}

	held, err := b.passphraseHeld(ctx, name)
	if err != nil {
		return err
	}
	if held {
		return nil
	}

	minted := make([]byte, passphraseBytes)
	if _, err := rand.Read(minted); err != nil {
		return fmt.Errorf("mint the passphrase %s holds: %w", name, err)
	}
	_, err = attempted(ctx, service.Projects.Secrets.AddVersion(secretPath(b.clients.project, name), &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString(minted)},
	}).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("write the passphrase into %s: %w", name, err)
	}
	return nil
}

func (b bootstrapper) makeDatabase(ctx context.Context, read survey) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	operation, err := attempted(ctx, service.Projects.Databases.Create("projects/"+read.Project, &firestoreadmin.GoogleFirestoreAdminV1Database{
		LocationId:            read.Region,
		Type:                  nativeFirestore,
		DeleteProtectionState: protectionOn,
	}).DatabaseId(recordDatabase).Context(ctx).Do)
	if answeredCode(err) == http.StatusConflict {
		return b.databaseServesThisRegion(ctx, read)
	}
	if err != nil {
		return fmt.Errorf("create the %q Firestore database: %w", recordDatabase, err)
	}
	return b.awaited(ctx, fmt.Sprintf("creating the %q Firestore database", recordDatabase), operation)
}

func (b bootstrapper) databaseServesThisRegion(ctx context.Context, read survey) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	held, err := attempted(ctx, service.Projects.Databases.Get(databasePath(read.Project)).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("read the %q Firestore database that already stands: %w", recordDatabase, err)
	}
	if held.LocationId == read.Region {
		return b.protectDatabase(ctx)
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"project %s already holds the %q Firestore database in %s and Firestore never moves one, "+
			"so the records this bootstrap writes would sit a continent away from the buckets and keys it names %s for.\n"+
			"Bootstrap this project in %s, or deploy into a project with no %q database",
		read.Project, recordDatabase, held.LocationId, read.Region, held.LocationId, recordDatabase)
}

func (b bootstrapper) protectDatabase(ctx context.Context) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	operation, err := attempted(ctx, service.Projects.Databases.Patch(databasePath(b.clients.project),
		&firestoreadmin.GoogleFirestoreAdminV1Database{DeleteProtectionState: protectionOn}).
		UpdateMask("deleteProtectionState").Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("hold the %q Firestore database under delete protection: %w", recordDatabase, err)
	}
	return b.awaited(ctx, fmt.Sprintf("holding the %q Firestore database under delete protection", recordDatabase), operation)
}

func (b bootstrapper) awaited(ctx context.Context, doing string, operation *firestoreadmin.GoogleLongrunningOperation) error {
	if operation == nil || operation.Done || operation.Name == "" {
		return operationFailed(doing, operation)
	}
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	finished, err := until(ctx, doing, func() (*firestoreadmin.GoogleLongrunningOperation, error) {
		return attempted(ctx, service.Projects.Databases.Operations.Get(operation.Name).Context(ctx).Do)
	}, func(held *firestoreadmin.GoogleLongrunningOperation) bool { return held.Done })
	if err != nil {
		return fmt.Errorf("wait for %s: %w", doing, err)
	}
	return operationFailed(doing, finished)
}

func operationFailed(doing string, operation *firestoreadmin.GoogleLongrunningOperation) error {
	if operation == nil || operation.Error == nil {
		return nil
	}
	return fmt.Errorf("%s: %s", doing, operation.Error.Message)
}

type removal struct {
	item   item
	action providerkit.ChangeAction
	reason string
}

func (b bootstrapper) PlanRemoval(ctx context.Context, class providerkit.Class) (providerkit.Plan, error) {
	read, err := b.survey(ctx, class)
	if err != nil {
		return providerkit.Plan{}, err
	}
	stack, params := providerkit.ChangeGroup{
		Kind:   providerkit.StackGroupKind,
		Name:   read.Project + "/" + string(class),
		Action: providerkit.ActionDelete,
	}, providerkit.ChangeGroup{
		Kind:   providerkit.ParameterGroupKind,
		Name:   providerkit.ParameterGroupKind,
		Action: providerkit.ActionDelete,
	}
	for _, taking := range removals(read) {
		change := providerkit.Change{
			Kind:   taking.item.Kind,
			Name:   taking.item.Name,
			Action: taking.action,
			Reason: taking.reason,
			Slow:   taking.item.Slow,
		}
		if taking.item.Kind == KindSecret {
			params.Changes = append(params.Changes, change)
			continue
		}
		stack.Changes = append(stack.Changes, change)
	}
	params.Action, params.Reason = providerkit.RollUp(params.Changes)
	return providerkit.Plan{Groups: providerkit.Vendored(Vendor, []providerkit.ChangeGroup{stack, params})}, nil
}

func removals(read survey) []removal {
	items := bootstrapItems(read.Project, read.Class)
	byKind := map[string]item{}
	for _, held := range items {
		byKind[held.Kind] = held
	}
	ordered := []item{
		byKind[KindSecret],
		byKind[KindKey],
		byKind[KindKeyRing],
		byKind[KindDatabase],
		{Kind: KindBucket, Name: StateBucketName(read.Project, read.Class)},
		{Kind: KindBucket, Name: BucketName(read.Project, read.Class)},
	}

	out := make([]removal, 0, len(ordered))
	for _, held := range ordered {
		out = append(out, removing(read, held))
	}
	return out
}

func removing(read survey, held item) removal {
	taking := removal{item: held, action: providerkit.ActionDelete}
	switch {
	case held.Kind == KindKeyRing:
		taking.action, taking.reason = providerkit.ActionKeep, reasonRingKept
	case held.Kind == KindDatabase && read.sibling:
		taking.action, taking.reason = providerkit.ActionKeep, fmt.Sprintf(reasonShared, siblingOf(read.Class))
	case held.Kind == KindDatabase && read.Emulated:
		taking.action, taking.reason = providerkit.ActionKeep, reasonEmulated
	case held.Kind == KindDatabase:
		taking.action = providerkit.ActionDisableThenDelete
	case held.Kind == KindKey:
		taking.reason, taking.item.Slow = reasonDestroy, true
	}
	if taking.action != providerkit.ActionKeep && !read.holds(taking.item) {
		taking.action, taking.reason = providerkit.ActionKeep, "nothing stands here"
	}
	return taking
}

func (b bootstrapper) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
	read, err := b.survey(ctx, class)
	if err != nil {
		return err
	}
	if read.stateOf != "" {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s still holds the state of %s, and removing the bucket a stack is recorded in strands what that stack stood up.\n"+
				"Run `%s` in every project deployed here first",
			StateBucketName(read.Project, class), read.stateOf, destroyIn(class))
	}
	if read.Present {
		leaving := read.Stamp
		leaving.State = stateRemoving
		if _, err := b.stampWith(ctx, read, leaving, read.Generation); err != nil {
			return err
		}
	}
	for _, taking := range removals(read) {
		if taking.action == providerkit.ActionKeep {
			say(report, "kept "+taking.item.ID()+": "+taking.reason)
			continue
		}
		if err := b.take(ctx, read, taking.item); err != nil {
			return err
		}
		say(report, "removed "+taking.item.ID())
	}
	return nil
}

func destroyIn(class providerkit.Class) string {
	if class == providerkit.ClassPreview {
		return "ocel destroy preview"
	}
	return "ocel destroy production"
}

func (b bootstrapper) take(ctx context.Context, read survey, held item) error {
	switch held.Kind {
	case KindSecret:
		return b.takeSecret(ctx, held.Name)
	case KindKey:
		return b.takeKey(ctx, held.Name)
	case KindDatabase:
		return b.takeDatabase(ctx, read)
	case KindBucket:
		return b.takeBucket(ctx, held.Name)
	default:
		return providerkit.Refuse(providerkit.CodeInvalid, "gcp: nothing takes down a %s", held.Kind)
	}
}

func (b bootstrapper) takeSecret(ctx context.Context, name string) error {
	service, err := b.clients.Secrets()
	if err != nil {
		return err
	}
	if _, err := attempted(ctx, service.Projects.Secrets.Delete(secretPath(b.clients.project, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	return nil
}

func (b bootstrapper) takeKey(ctx context.Context, name string) error {
	client, err := b.clients.KMS()
	if err != nil {
		return err
	}
	key, err := b.keyHeld(ctx, name)
	if err != nil {
		return err
	}
	if key == nil {
		return nil
	}
	if !usable(key.GetPrimary()) {
		return nil
	}
	if _, err := dialled(ctx, func() (*kmspb.CryptoKeyVersion, error) {
		return client.DestroyCryptoKeyVersion(ctx, &kmspb.DestroyCryptoKeyVersionRequest{
			Name: key.GetPrimary().GetName(),
		})
	}); err != nil {
		return fmt.Errorf("schedule the %s key's material for destruction: %w", name, err)
	}
	return nil
}

func (b bootstrapper) takeDatabase(ctx context.Context, read survey) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	path := databasePath(read.Project)
	lifting, err := attempted(ctx, service.Projects.Databases.Patch(path, &firestoreadmin.GoogleFirestoreAdminV1Database{
		DeleteProtectionState: protectionOff,
	}).UpdateMask("deleteProtectionState").Context(ctx).Do)
	if absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lift delete protection from the %q Firestore database: %w", recordDatabase, err)
	}
	lifted := fmt.Sprintf("lifting delete protection from the %q Firestore database", recordDatabase)
	if err := b.awaited(ctx, lifted, lifting); err != nil {
		return err
	}

	deleting, err := attempted(ctx, service.Projects.Databases.Delete(path).Context(ctx).Do)
	if absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the %q Firestore database: %w", recordDatabase, err)
	}
	return b.awaited(ctx, fmt.Sprintf("deleting the %q Firestore database", recordDatabase), deleting)
}

func (b bootstrapper) takeBucket(ctx context.Context, name string) error {
	client, err := b.clients.Storage()
	if err != nil {
		return err
	}
	bucket := client.Bucket(name)
	held := bucket.Objects(ctx, &storage.Query{Versions: true})
	for {
		attrs, err := held.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
				return nil
			}
			return fmt.Errorf("list what %s holds: %w", name, err)
		}
		object := bucket.Object(attrs.Name)
		if attrs.Generation != 0 {
			object = object.Generation(attrs.Generation)
		}
		if err := done(ctx, func() error { return object.Delete(ctx) }); err != nil &&
			!errors.Is(err, storage.ErrObjectNotExist) && !absent(err) {
			return fmt.Errorf("empty %s: %w", name, err)
		}
	}
	if err := done(ctx, func() error { return bucket.Delete(ctx) }); err != nil &&
		!errors.Is(err, storage.ErrBucketNotExist) && !absent(err) {
		return fmt.Errorf("delete the %s bucket: %w", name, err)
	}
	return nil
}

func say(report providerkit.Reporter, message string) {
	if report != nil {
		report.Say(message)
	}
}
