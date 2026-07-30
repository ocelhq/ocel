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
)

// ErrNotFound reports that no value is set at a coordinate. Callers
// distinguish it from an empty value, which is a value.
var ErrNotFound = errors.New("no value is set there")

// ErrStaleVersion reports that a write's expectation about the current version
// no longer held when it committed: another writer got there first. The write
// is rejected rather than applied, so neither edit is silently lost.
var ErrStaleVersion = errors.New("the value changed since it was read; re-read it and try again")

// DynamoAPI is the subset of the DynamoDB client the store uses. Every
// consumer operation maps to exactly one of these calls — a point read, a
// prefix query, a transaction, or a point delete. Nothing here scans.
type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
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

// Version is one entry of a cell's change history.
type Version struct {
	Version   int64
	CreatedAt int64
	Size      int64
	Plaintext string
}

// item is one stored row. Current values and history entries share a shape:
// a history entry is what a current value was, so giving them different
// attributes would only mean two ways to read the same thing.
type item struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	Version    int64  `dynamodbav:"version"`
	Ciphertext []byte `dynamodbav:"ciphertext"`
	Size       int64  `dynamodbav:"size"`
	Ts         int64  `dynamodbav:"ts"`
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
	pk := partitionKey(c.Slug, s.Class)

	current, err := s.read(ctx, pk, currentSortKey(key))
	if err != nil {
		return Metadata{}, err
	}
	if expected != nil && *expected != current.Version {
		return Metadata{}, ErrStaleVersion
	}

	ciphertext, err := s.encrypt(ctx, plaintext)
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
	stored, err := s.read(ctx, partitionKey(c.Slug, s.Class), currentSortKey(c.canonical()))
	if err != nil {
		return Value{}, err
	}
	if stored.Version == 0 {
		return Value{}, ErrNotFound
	}

	value := Value{Metadata: Metadata{Coordinate: c, Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size}}
	if reveal {
		if value.Plaintext, err = s.decrypt(ctx, stored.Ciphertext); err != nil {
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
	items, err := s.query(ctx, partitionKey(slug, s.Class), currentPrefix, true)
	if err != nil {
		return nil, err
	}

	out := make([]Metadata, 0, len(items))
	for _, stored := range items {
		c, err := parseCurrentSortKey(slug, stored.SK)
		if err != nil {
			return nil, err
		}
		out = append(out, Metadata{Coordinate: c, Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size})
	}
	return out, nil
}

// Delete removes a cell's current pointer, reporting whether there was one. It
// leaves the version history: history records what the value was, and deleting
// the value does not unmake that.
func (s *Store) Delete(ctx context.Context, c Coordinate) (bool, error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	out, err := s.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:    aws.String(s.Table),
		Key:          pointKey(partitionKey(c.Slug, s.Class), currentSortKey(c.canonical())),
		ReturnValues: ddbtypes.ReturnValueAllOld,
	})
	if err != nil {
		return false, fmt.Errorf("delete %s: %w", c.Key, err)
	}
	return len(out.Attributes) > 0, nil
}

// Versions reads a cell's change history, newest first and no deeper than the
// window each write prunes to.
func (s *Store) Versions(ctx context.Context, c Coordinate, reveal bool) ([]Version, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	items, err := s.query(ctx, partitionKey(c.Slug, s.Class), historyPrefix(c.canonical()), false)
	if err != nil {
		return nil, err
	}

	out := make([]Version, 0, len(items))
	for _, stored := range items {
		version, err := parseHistorySortKey(stored.SK)
		if err != nil {
			return nil, err
		}
		entry := Version{Version: version, CreatedAt: stored.Ts, Size: stored.Size}
		if reveal {
			if entry.Plaintext, err = s.decrypt(ctx, stored.Ciphertext); err != nil {
				return nil, err
			}
		}
		out = append(out, entry)
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
// listing is never silently truncated.
func (s *Store) query(ctx context.Context, pk, prefix string, ascending bool) ([]item, error) {
	var out []item
	var start map[string]ddbtypes.AttributeValue
	for {
		page, err := s.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.Table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":pk":     &ddbtypes.AttributeValueMemberS{Value: pk},
				":prefix": &ddbtypes.AttributeValueMemberS{Value: prefix},
			},
			ScanIndexForward:  aws.Bool(ascending),
			ExclusiveStartKey: start,
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

func (s *Store) encrypt(ctx context.Context, plaintext string) ([]byte, error) {
	out, err := s.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(s.KeyARN),
		Plaintext: []byte(plaintext),
	})
	if err != nil {
		return nil, fmt.Errorf("encrypt value: %w", err)
	}
	return out.CiphertextBlob, nil
}

func (s *Store) decrypt(ctx context.Context, ciphertext []byte) (string, error) {
	out, err := s.KMS.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          aws.String(s.KeyARN),
		CiphertextBlob: ciphertext,
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
// is six fields wide and fixed, and a hand-written map is what the fakes and
// the condition expressions read.
func marshal(i item) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk":         &ddbtypes.AttributeValueMemberS{Value: i.PK},
		"sk":         &ddbtypes.AttributeValueMemberS{Value: i.SK},
		"version":    number(i.Version),
		"ciphertext": &ddbtypes.AttributeValueMemberB{Value: i.Ciphertext},
		"size":       number(i.Size),
		"ts":         number(i.Ts),
	}
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
