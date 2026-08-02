package vars

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"golang.org/x/sync/errgroup"
)

// revealConcurrency caps the decrypts a batched read has in flight. A function
// declares a handful of live values, not thousands, so this is a guard against
// a pathological manifest rather than a tuned number.
const revealConcurrency = 16

// ErrNotFound reports that no value is set at a coordinate. Callers
// distinguish it from an empty value, which is a value.
var ErrNotFound = errors.New("no value is set there")

// ErrStaleVersion reports that a write's expectation about the current version
// no longer held when it committed: another writer got there first. The write
// is rejected rather than applied, so neither edit is silently lost.
var ErrStaleVersion = errors.New("the value changed since it was read; re-read it and try again")

// DynamoAPI is the subset of the DynamoDB client the store uses. Every
// consumer operation maps to exactly one of these calls — a point read, a
// prefix query, a transaction, or a point write. Nothing here scans.
type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

// CryptoAPI is the subset of the KMS client the store uses. Values are
// encrypted directly under the class's key rather than under a wrapped data
// key: a variable is bounded by MaxValueBytes, which KMS encrypts in one call,
// so an envelope would add a wrapped key and an AEAD to buy headroom nothing
// needs.
type CryptoAPI interface {
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// Store is one substrate's variable store: its table and the one key its class
// encrypts under.
type Store struct {
	Dynamo DynamoAPI
	KMS    CryptoAPI
	Table  string
	KeyARN string
	Class  string
	Now    func() time.Time
}

// Metadata is everything about a value except the value.
//
// Target is the cell a reference points at, and the zero Coordinate for a cell
// holding a value of its own. Version, UpdatedAt and Size stay the row's own
// either way: a reference has no value of its own, so it has no size of its
// own, and the version a write against it must expect is the pointer's.
type Metadata struct {
	Coordinate Coordinate
	Version    int64
	UpdatedAt  int64
	Size       int64
	Target     Coordinate
}

// Value is a cell's metadata, with its plaintext when the read revealed it.
type Value struct {
	Metadata
	Plaintext string
}

// Version is one entry of a cell's change history. It carries no plaintext:
// history records that a value changed, not what it was.
type Version struct {
	Version   int64
	CreatedAt int64
	Size      int64
}

// item is one stored row. Current values and history entries share a shape:
// a history entry is what a current value was, so giving them different
// attributes would only mean two ways to read the same thing.
//
// Deleted marks the one row that is not a value: the tombstone a delete leaves
// at a cell's current pointer. It carries the version the cell had reached and
// nothing of what the value was, which is what keeps the next write's version
// number ahead of every history row instead of back on top of one.
//
// TargetPK and TargetSK are the other row that is not a value: a reference,
// whose content is the address of the cell holding the value. They are the
// reverse index's own key attributes rather than a pair beside them, so what a
// reference points at and what the index answers by are one thing that cannot
// disagree — and so the index stays sparse, since every other row leaves them
// off entirely.
type item struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	Version    int64  `dynamodbav:"version"`
	Ciphertext []byte `dynamodbav:"ciphertext"`
	Size       int64  `dynamodbav:"size"`
	Ts         int64  `dynamodbav:"ts"`
	Deleted    bool   `dynamodbav:"deleted"`
	TargetPK   string `dynamodbav:"gsi1pk"`
	TargetSK   string `dynamodbav:"gsi1sk"`
}

// references reports whether the row is a reference rather than a value.
func (i item) references() bool { return i.TargetPK != "" && i.TargetSK != "" }

// target is the cell this row points at, or the zero Coordinate when it holds a
// value of its own.
func (i item) target() (Coordinate, error) {
	if !i.references() {
		return Coordinate{}, nil
	}
	slug, err := parsePartitionKey(i.TargetPK)
	if err != nil {
		return Coordinate{}, err
	}
	return parseCurrentSortKey(slug, i.TargetSK)
}

// liveVersion is the version a reader can observe: zero when the cell holds no
// value, whether it never held one or holds a tombstone.
func (i item) liveVersion() int64 {
	if i.Deleted {
		return 0
	}
	return i.Version
}

func (s *Store) now() int64 {
	if s.Now == nil {
		return time.Now().Unix()
	}
	return s.Now().Unix()
}

// Set writes a value, as one version, through commit.
//
// A cell holding a reference is refused rather than overwritten: a reference
// has no value of its own, so there is exactly one place the value behind it
// can be changed. Pointing the cell somewhere else is SetReference's business,
// and keeping the value it holds is a delete away.
//
// expected is the version the caller believes is current: nil for a blind
// write, which still loses to a writer that commits between this read and this
// commit; zero to require that the cell is unset.
func (s *Store) Set(ctx context.Context, c Coordinate, plaintext string, expected *int64) (Metadata, error) {
	if err := c.validate(); err != nil {
		return Metadata{}, err
	}
	if len(plaintext) > MaxValueBytes {
		return Metadata{}, fmt.Errorf("value for %s is too large: %d bytes, limit %d", c.Key, len(plaintext), MaxValueBytes)
	}

	key := c.canonical()
	current, err := s.read(ctx, PartitionKey(c.Slug, s.Class), currentSortKey(key))
	if err != nil {
		return Metadata{}, err
	}
	if current.references() {
		target, err := current.target()
		if err != nil {
			return Metadata{}, err
		}
		return Metadata{}, fmt.Errorf("%s is a reference to %s, which is where that value is edited: %w", c, target, ErrIsReference)
	}

	ciphertext, err := s.encrypt(ctx, key, plaintext)
	if err != nil {
		return Metadata{}, err
	}
	return s.commit(ctx, c, current, expected, item{Ciphertext: ciphertext, Size: int64(len(plaintext))})
}

// commit writes one version of a cell as a transaction: it records the new
// version, updates the cell's current pointer under a condition on the version
// it read, and deletes the version that falls out of the retention window. The
// pruned version is computed from the new one, never queried for.
//
// content is what the row holds — ciphertext and its size for a value, the
// target's address for a reference — and everything else about the row is the
// same either way, which is what keeps one version sequence per cell however
// its content changes shape.
//
// The history row is content minus the index keys. A version records that a
// cell changed, and an indexed history row would answer the reverse lookup with
// every version that ever pointed somewhere rather than with what points there
// now.
func (s *Store) commit(ctx context.Context, c Coordinate, current item, expected *int64, content item) (Metadata, error) {
	if expected != nil && *expected != current.liveVersion() {
		return Metadata{}, ErrStaleVersion
	}

	key := c.canonical()
	pk := PartitionKey(c.Slug, s.Class)
	next := current.Version + 1

	written := content
	written.PK = pk
	written.Version = next
	written.Ts = s.now()

	historyItem := written
	historyItem.SK = historySortKey(key, next)
	historyItem.TargetPK, historyItem.TargetSK = "", ""
	currentItem := written
	currentItem.SK = currentSortKey(key)

	writes := []ddbtypes.TransactWriteItem{
		{Put: &ddbtypes.Put{TableName: aws.String(s.Table), Item: marshal(historyItem)}},
		{Put: &ddbtypes.Put{
			TableName:                aws.String(s.Table),
			Item:                     marshal(currentItem),
			ConditionExpression:      aws.String("attribute_not_exists(pk) OR #version = :seen"),
			ExpressionAttributeNames: map[string]string{"#version": "version"},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":seen": number(current.Version),
			},
		}},
	}
	if pruned := next - historyWindow; pruned > 0 {
		writes = append(writes, ddbtypes.TransactWriteItem{Delete: &ddbtypes.Delete{
			TableName: aws.String(s.Table),
			Key:       pointKey(pk, historySortKey(key, pruned)),
		}})
	}

	if _, err := s.Dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes}); err != nil {
		var cancelled *ddbtypes.TransactionCanceledException
		if errors.As(err, &cancelled) {
			return Metadata{}, ErrStaleVersion
		}
		return Metadata{}, fmt.Errorf("write %s: %w", c.Key, err)
	}

	target, err := currentItem.target()
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{Coordinate: c, Version: next, UpdatedAt: currentItem.Ts, Size: currentItem.Size, Target: target}, nil
}

// Get reads one cell. It decrypts only when reveal asks it to, so a routine
// read discloses nothing and leaves no decrypt in the key's audit trail.
//
// A cell holding a reference reads as the value it points at, resolved now
// rather than copied earlier, so an edit at the source is visible to every
// consumer the moment it lands. Its metadata stays its own: what a reference
// borrows is the plaintext, not the row.
func (s *Store) Get(ctx context.Context, c Coordinate, reveal bool) (Value, error) {
	if err := c.validate(); err != nil {
		return Value{}, err
	}
	stored, err := s.read(ctx, PartitionKey(c.Slug, s.Class), currentSortKey(c.canonical()))
	if err != nil {
		return Value{}, err
	}
	if stored.liveVersion() == 0 {
		return Value{}, ErrNotFound
	}
	target, err := stored.target()
	if err != nil {
		return Value{}, err
	}

	value := Value{Metadata: Metadata{Coordinate: c, Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size, Target: target}}
	if !reveal {
		return value, nil
	}
	holder, at, err := s.dereference(ctx, c, stored)
	if err != nil {
		return Value{}, err
	}
	if value.Plaintext, err = s.decrypt(ctx, at, holder.Ciphertext); err != nil {
		return Value{}, err
	}
	return value, nil
}

// dereference is read-time resolution: the row actually holding the plaintext,
// and the coordinate its ciphertext is bound to. A row that holds a value is
// its own answer, and a reference is followed exactly once.
//
// A follow that lands on another reference is refused rather than followed
// again. The write path refuses to deepen a chain, but half of that guard reads
// the reverse index and no index is read consistently, so a chain is improbable
// rather than impossible; a second follow would be a read path with no bound on
// it, over partitions a function's grant does not name.
//
// The follow is a query rather than a point read because a function's grant on
// the table is Query alone, and resolution must be the same operation wherever
// it runs: a read path the runtime cannot emit is one that works for the CLI
// and fails in production.
func (s *Store) dereference(ctx context.Context, at Coordinate, stored item) (item, Coordinate, error) {
	target, err := stored.target()
	if err != nil {
		return item{}, Coordinate{}, err
	}
	if target.Slug == "" {
		return stored, at, nil
	}
	holder, err := s.follow(ctx, stored.TargetPK, stored.TargetSK)
	if err != nil {
		return item{}, Coordinate{}, err
	}
	if holder.liveVersion() == 0 {
		return item{}, Coordinate{}, fmt.Errorf("%s references %s, which holds no value: %w", at, target, ErrNotFound)
	}
	if holder.references() {
		return item{}, Coordinate{}, fmt.Errorf("%s references %s, which is itself a reference: %w", at, target, ErrWouldDeepen)
	}
	return holder, target, nil
}

// follow reads the one row a reference points at. The sort key is exact, but a
// query matches a prefix, so the row is picked out by equality rather than
// trusted from the result: an environment named "*x" would otherwise be read as
// the class-wide value it merely begins with.
func (s *Store) follow(ctx context.Context, pk, sk string) (item, error) {
	items, err := s.query(ctx, pk, sk, true)
	if err != nil {
		return item{}, err
	}
	for _, stored := range items {
		if stored.SK == sk {
			return stored, nil
		}
	}
	return item{}, nil
}

// List enumerates a project's current values as metadata. It never reveals and
// never reaches into history: both follow from the current namespace being its
// own sort-key prefix.
func (s *Store) List(ctx context.Context, slug string) ([]Metadata, error) {
	if slug == "" {
		return nil, fmt.Errorf("a project slug is required")
	}
	items, err := s.query(ctx, PartitionKey(slug, s.Class), currentPrefix, true)
	if err != nil {
		return nil, err
	}

	out := make([]Metadata, 0, len(items))
	for _, stored := range items {
		if stored.Deleted {
			continue
		}
		c, err := parseCurrentSortKey(slug, stored.SK)
		if err != nil {
			return nil, err
		}
		target, err := stored.target()
		if err != nil {
			return nil, err
		}
		out = append(out, Metadata{Coordinate: c, Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size, Target: target})
	}
	return out, nil
}

// Reveal reads a named set of a project's cells and returns their plaintext.
// It is the runtime's read: a function resolving its live-class values wants
// every one of them at once, on the cold path, so the cost is one query over
// the project's current values plus one decrypt per cell. The decrypts run
// concurrently because KMS has no batch decrypt and each cell is sealed under
// its own coordinate's context, so N keys are genuinely N calls and only their
// latency need be shared.
//
// A named cell that holds no value is absent from the result rather than an
// error: what a missing value means is the declaring schema's business, and it
// is decided where the schema is. A cell that is present but fails to resolve —
// a reference whose target has since been deleted, or a value that fails to
// decrypt — is an error for the whole batch: half a set of variables is an
// application with one silently unset, which at the point of use reads as one
// that was never required.
func (s *Store) Reveal(ctx context.Context, slug string, cells []Coordinate) ([]Value, error) {
	if slug == "" {
		return nil, fmt.Errorf("a project slug is required")
	}
	if len(cells) == 0 {
		return nil, nil
	}

	wanted := make(map[string]Coordinate, len(cells))
	for _, c := range cells {
		c.Slug = slug
		if err := c.validate(); err != nil {
			return nil, err
		}
		wanted[currentSortKey(c.canonical())] = c
	}

	items, err := s.query(ctx, PartitionKey(slug, s.Class), currentPrefix, true)
	if err != nil {
		return nil, err
	}

	var found []item
	var at []Coordinate
	for _, stored := range items {
		c, ok := wanted[stored.SK]
		if !ok || stored.Deleted {
			continue
		}
		found = append(found, stored)
		at = append(at, c)
	}

	values := make([]Value, len(found))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, stored := range found {
		group.Go(func() error {
			holder, from, err := s.dereference(ctx, at[i], stored)
			if err != nil {
				return err
			}
			plaintext, err := s.decrypt(ctx, from, holder.Ciphertext)
			if err != nil {
				return err
			}
			target, err := stored.target()
			if err != nil {
				return err
			}
			values[i] = Value{
				Metadata:  Metadata{Coordinate: at[i], Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size, Target: target},
				Plaintext: plaintext,
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return values, nil
}

// Delete unsets a cell, reporting whether there was a value to unset. It
// leaves the version history: history records what the value was, and deleting
// the value does not unmake that.
//
// The pointer is replaced by a tombstone rather than removed. Dropping it
// would take the cell's version number with it, and the next write — which
// derives its version from the pointer — would restart at one and land on top
// of the history row that version already holds. The tombstone keeps the
// number and drops the ciphertext, so the value is gone but the sequence is
// not. It does not consume a version of its own: every version number stays
// backed by exactly one history row, which is what the computed prune relies
// on.
//
// expected is read exactly as Set reads it: nil for a blind delete, zero to
// require that no value is set — which an unset cell already satisfies, so
// repeating a delete that landed stays idempotent. The tombstone is a write
// rather than a removal, so the expectation is enforced the way a write's is:
// by the condition the write itself carries, not by the read alone.
func (s *Store) Delete(ctx context.Context, c Coordinate, expected *int64) (bool, error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	pk, sk := PartitionKey(c.Slug, s.Class), currentSortKey(c.canonical())
	current, err := s.read(ctx, pk, sk)
	if err != nil {
		return false, err
	}
	if expected != nil && *expected != current.liveVersion() {
		return false, ErrStaleVersion
	}
	if current.liveVersion() == 0 {
		return false, nil
	}

	tombstone := item{PK: pk, SK: sk, Version: current.Version, Ts: s.now(), Deleted: true}
	if _, err := s.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                aws.String(s.Table),
		Item:                     marshal(tombstone),
		ConditionExpression:      aws.String("attribute_not_exists(pk) OR #version = :seen"),
		ExpressionAttributeNames: map[string]string{"#version": "version"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":seen": number(current.Version),
		},
	}); err != nil {
		var failed *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return false, ErrStaleVersion
		}
		return false, fmt.Errorf("delete %s: %w", c.Key, err)
	}
	return true, nil
}

// Versions reads a cell's change history, newest first and no deeper than the
// window each write prunes to. It never decrypts: the ciphertext of a version
// nobody can name is not a thing this call can hand back, so a reveal here
// could only ever be the whole window at once. Reading one value back is
// Get's job, against the cell the caller named.
func (s *Store) Versions(ctx context.Context, c Coordinate) ([]Version, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	items, err := s.query(ctx, PartitionKey(c.Slug, s.Class), historyPrefix(c.canonical()), false)
	if err != nil {
		return nil, err
	}

	out := make([]Version, 0, len(items))
	for _, stored := range items {
		version, err := parseHistorySortKey(stored.SK)
		if err != nil {
			return nil, err
		}
		out = append(out, Version{Version: version, CreatedAt: stored.Ts, Size: stored.Size})
	}
	return out, nil
}

// read returns the item at a point, or the zero item when there is none.
func (s *Store) read(ctx context.Context, pk, sk string) (item, error) {
	out, err := s.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.Table),
		Key:            pointKey(pk, sk),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return item{}, fmt.Errorf("read %s: %w", sk, err)
	}
	if len(out.Item) == 0 {
		return item{}, nil
	}
	return unmarshal(out.Item)
}

// query reads every item under a sort-key prefix, following pagination so a
// listing is never silently truncated. An empty prefix reads the whole
// partition, which is what a project's teardown removes.
func (s *Store) query(ctx context.Context, pk, prefix string, ascending bool) ([]item, error) {
	condition := "pk = :pk"
	values := map[string]ddbtypes.AttributeValue{":pk": &ddbtypes.AttributeValueMemberS{Value: pk}}
	if prefix != "" {
		condition += " AND begins_with(sk, :prefix)"
		values[":prefix"] = &ddbtypes.AttributeValueMemberS{Value: prefix}
	}

	var out []item
	var start map[string]ddbtypes.AttributeValue
	for {
		page, err := s.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.Table),
			KeyConditionExpression:    aws.String(condition),
			ExpressionAttributeValues: values,
			ScanIndexForward:          aws.Bool(ascending),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", prefix, err)
		}
		for _, raw := range page.Items {
			stored, err := unmarshal(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, stored)
		}
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

// encryptionContext binds a ciphertext to the one cell it was written for. The
// key is per environment class and serves every project in it, so without this
// the bytes are all a decrypt needs: a blob moved to another cell, or to
// another project sharing the class key, would open as that cell's value. KMS
// authenticates the context on decrypt, so a relocated blob fails to open
// instead. The canonical coordinate is what is bound, so root and class-wide
// name their sentinels rather than binding to nothing.
func encryptionContext(c Coordinate) map[string]string {
	c = c.canonical()
	return map[string]string{
		"slug":        c.Slug,
		"folder":      c.Folder,
		"key":         c.Key,
		"environment": c.Environment,
	}
}

func (s *Store) encrypt(ctx context.Context, c Coordinate, plaintext string) ([]byte, error) {
	out, err := s.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(s.KeyARN),
		Plaintext:         []byte(plaintext),
		EncryptionContext: encryptionContext(c),
	})
	if err != nil {
		return nil, fmt.Errorf("encrypt value: %w", err)
	}
	return out.CiphertextBlob, nil
}

func (s *Store) decrypt(ctx context.Context, c Coordinate, ciphertext []byte) (string, error) {
	out, err := s.KMS.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(s.KeyARN),
		CiphertextBlob:    ciphertext,
		EncryptionContext: encryptionContext(c),
	})
	if err != nil {
		return "", fmt.Errorf("decrypt value: %w", err)
	}
	return string(out.Plaintext), nil
}

func pointKey(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk": &ddbtypes.AttributeValueMemberS{Value: pk},
		"sk": &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

func number(n int64) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

// marshal renders an item by hand rather than by reflection: the attribute set
// is narrow and fixed, and a hand-written map is what the fakes and the
// condition expressions read. A tombstone carries no ciphertext attribute at
// all, so the value is absent rather than empty.
func marshal(i item) map[string]ddbtypes.AttributeValue {
	m := map[string]ddbtypes.AttributeValue{
		"pk":      &ddbtypes.AttributeValueMemberS{Value: i.PK},
		"sk":      &ddbtypes.AttributeValueMemberS{Value: i.SK},
		"version": number(i.Version),
		"size":    number(i.Size),
		"ts":      number(i.Ts),
		"deleted": &ddbtypes.AttributeValueMemberBOOL{Value: i.Deleted},
	}
	if len(i.Ciphertext) > 0 {
		m["ciphertext"] = &ddbtypes.AttributeValueMemberB{Value: i.Ciphertext}
	}
	// The index attributes are written only by a reference, which is the whole
	// of what keeps the reverse index sparse: every other row is absent from it
	// rather than indexed under an empty address.
	if i.references() {
		m["gsi1pk"] = &ddbtypes.AttributeValueMemberS{Value: i.TargetPK}
		m["gsi1sk"] = &ddbtypes.AttributeValueMemberS{Value: i.TargetSK}
	}
	return m
}

func unmarshal(raw map[string]ddbtypes.AttributeValue) (item, error) {
	i := item{}
	for name, field := range map[string]*string{"pk": &i.PK, "sk": &i.SK, "gsi1pk": &i.TargetPK, "gsi1sk": &i.TargetSK} {
		if v, ok := raw[name].(*ddbtypes.AttributeValueMemberS); ok {
			*field = v.Value
		}
	}
	if v, ok := raw["ciphertext"].(*ddbtypes.AttributeValueMemberB); ok {
		i.Ciphertext = v.Value
	}
	if v, ok := raw["deleted"].(*ddbtypes.AttributeValueMemberBOOL); ok {
		i.Deleted = v.Value
	}
	for name, field := range map[string]*int64{"version": &i.Version, "size": &i.Size, "ts": &i.Ts} {
		v, ok := raw[name].(*ddbtypes.AttributeValueMemberN)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(v.Value, 10, 64)
		if err != nil {
			return item{}, fmt.Errorf("read %s of %s: %w", name, i.SK, err)
		}
		*field = n
	}
	return i, nil
}
