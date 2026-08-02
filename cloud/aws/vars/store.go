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
type Metadata struct {
	Coordinate Coordinate
	Version    int64
	UpdatedAt  int64
	Size       int64
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
type item struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	Version    int64  `dynamodbav:"version"`
	Ciphertext []byte `dynamodbav:"ciphertext"`
	Size       int64  `dynamodbav:"size"`
	Ts         int64  `dynamodbav:"ts"`
	Deleted    bool   `dynamodbav:"deleted"`
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

// Set writes a value as one transaction: it records a new version, updates the
// cell's current pointer under a condition on the version it read, and deletes
// the version that falls out of the retention window. The pruned version is
// computed from the new one, never queried for.
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
	pk := PartitionKey(c.Slug, s.Class)

	current, err := s.read(ctx, pk, currentSortKey(key))
	if err != nil {
		return Metadata{}, err
	}
	if expected != nil && *expected != current.liveVersion() {
		return Metadata{}, ErrStaleVersion
	}

	ciphertext, err := s.encrypt(ctx, key, plaintext)
	if err != nil {
		return Metadata{}, err
	}

	next := current.Version + 1
	written := item{
		PK:         pk,
		Version:    next,
		Ciphertext: ciphertext,
		Size:       int64(len(plaintext)),
		Ts:         s.now(),
	}

	historyItem := written
	historyItem.SK = historySortKey(key, next)
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

	return Metadata{Coordinate: c, Version: next, UpdatedAt: currentItem.Ts, Size: currentItem.Size}, nil
}

// Get reads one cell. It decrypts only when reveal asks it to, so a routine
// read discloses nothing and leaves no decrypt in the key's audit trail.
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

	value := Value{Metadata: Metadata{Coordinate: c, Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size}}
	if reveal {
		if value.Plaintext, err = s.decrypt(ctx, c, stored.Ciphertext); err != nil {
			return Value{}, err
		}
	}
	return value, nil
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
		out = append(out, Metadata{Coordinate: c, Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size})
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
// is decided where the schema is. A cell that is present but fails to decrypt
// is an error for the whole batch — half a set of variables is an application
// with one silently unset, which at the point of use reads as one that was
// never required.
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
			plaintext, err := s.decrypt(ctx, at[i], stored.Ciphertext)
			if err != nil {
				return err
			}
			values[i] = Value{
				Metadata:  Metadata{Coordinate: at[i], Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size},
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
	return m
}

func unmarshal(raw map[string]ddbtypes.AttributeValue) (item, error) {
	i := item{}
	if v, ok := raw["pk"].(*ddbtypes.AttributeValueMemberS); ok {
		i.PK = v.Value
	}
	if v, ok := raw["sk"].(*ddbtypes.AttributeValueMemberS); ok {
		i.SK = v.Value
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
