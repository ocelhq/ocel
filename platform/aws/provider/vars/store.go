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

const revealConcurrency = 16

var ErrNotFound = errors.New("vars: not found")

var ErrStaleVersion = errors.New("vars: stale version")

type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

type CryptoAPI interface {
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

type Store struct {
	Dynamo DynamoAPI
	KMS    CryptoAPI
	Table  string
	KeyARN string
	Class  string
	Now    func() time.Time
}

type Metadata struct {
	Coordinate Coordinate
	Version    int64
	UpdatedAt  int64
	Size       int64
	Target     Coordinate
}

type Value struct {
	Metadata
	Plaintext string
}

type Version struct {
	Version   int64
	CreatedAt int64
	Size      int64
}

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

	Names  []string `dynamodbav:"links"`
	Record string   `dynamodbav:"record"`
	Owner  string   `dynamodbav:"owner"`
}

func (i item) references() bool { return i.TargetPK != "" && i.TargetSK != "" }

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
	holder, at, err := s.dereference(ctx, c, stored, nil)
	if err != nil {
		return Value{}, err
	}
	if value.Plaintext, err = s.decrypt(ctx, at, holder.Ciphertext); err != nil {
		return Value{}, err
	}
	return value, nil
}

func (s *Store) dereference(ctx context.Context, at Coordinate, stored item, held map[string]item) (item, Coordinate, error) {
	target, err := stored.target()
	if err != nil {
		return item{}, Coordinate{}, err
	}
	if target.Slug == "" {
		return stored, at, nil
	}
	holder, known := held[cellKey(stored.TargetPK, stored.TargetSK)]
	if !known {
		if holder, err = s.follow(ctx, stored.TargetPK, stored.TargetSK); err != nil {
			return item{}, Coordinate{}, err
		}
	}
	if holder.liveVersion() == 0 {
		return item{}, Coordinate{}, fmt.Errorf("%s references %s, which holds no value: %w", at, target, ErrNotFound)
	}
	if holder.references() {
		return item{}, Coordinate{}, fmt.Errorf("%s references %s, which is itself a reference: %w", at, target, ErrWouldDeepen)
	}
	return holder, target, nil
}

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

	held := make(map[string]item, len(items))
	var found []item
	var at []Coordinate
	for _, stored := range items {
		held[cellKey(stored.PK, stored.SK)] = stored
		c, ok := wanted[stored.SK]
		if !ok || stored.Deleted {
			continue
		}
		found = append(found, stored)
		at = append(at, c)
	}
	if err := s.gather(ctx, found, held); err != nil {
		return nil, err
	}

	var sealed []sealedValue
	slotOf := make([]int, len(found))
	slots := make(map[string]int, len(found))
	for i, stored := range found {
		holder, from, err := s.dereference(ctx, at[i], stored, held)
		if err != nil {
			return nil, err
		}
		key := cellKey(holder.PK, holder.SK)
		slot, seen := slots[key]
		if !seen {
			slot = len(sealed)
			slots[key] = slot
			sealed = append(sealed, sealedValue{at: from, ciphertext: holder.Ciphertext})
		}
		slotOf[i] = slot
	}

	plaintexts := make([]string, len(sealed))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, cell := range sealed {
		group.Go(func() error {
			plaintext, err := s.decrypt(ctx, cell.at, cell.ciphertext)
			if err != nil {
				return err
			}
			plaintexts[i] = plaintext
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	values := make([]Value, len(found))
	for i, stored := range found {
		target, err := stored.target()
		if err != nil {
			return nil, err
		}
		values[i] = Value{
			Metadata:  Metadata{Coordinate: at[i], Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size, Target: target},
			Plaintext: plaintexts[slotOf[i]],
		}
	}
	return values, nil
}

type sealedValue struct {
	at         Coordinate
	ciphertext []byte
}

func (s *Store) gather(ctx context.Context, cells []item, held map[string]item) error {
	var pending []item
	for _, stored := range cells {
		if !stored.references() {
			continue
		}
		key := cellKey(stored.TargetPK, stored.TargetSK)
		if _, known := held[key]; known {
			continue
		}
		held[key] = item{}
		pending = append(pending, stored)
	}
	if len(pending) == 0 {
		return nil
	}

	holders := make([]item, len(pending))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, stored := range pending {
		group.Go(func() error {
			holder, err := s.follow(ctx, stored.TargetPK, stored.TargetSK)
			if err != nil {
				return err
			}
			holders[i] = holder
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	for i, stored := range pending {
		held[cellKey(stored.TargetPK, stored.TargetSK)] = holders[i]
	}
	return nil
}

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

func (s *Store) query(ctx context.Context, pk, prefix string, ascending bool) ([]item, error) {
	return s.scan(ctx, pk, prefix, ascending, false)
}

func (s *Store) queryConsistent(ctx context.Context, pk, prefix string) ([]item, error) {
	return s.scan(ctx, pk, prefix, true, true)
}

func (s *Store) scan(ctx context.Context, pk, prefix string, ascending, consistent bool) ([]item, error) {
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
			ConsistentRead:            aws.Bool(consistent),
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

func encryptionContext(c Coordinate) map[string]string {
	c = c.canonical()
	ctx := map[string]string{
		"slug":        c.Slug,
		"folder":      c.Folder,
		"key":         c.Key,
		"environment": c.Environment,
	}
	if c.Link != "" {
		ctx["link"] = c.Link
	}
	return ctx
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

func cellKey(pk, sk string) string { return pk + "\x00" + sk }

func pointKey(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk": &ddbtypes.AttributeValueMemberS{Value: pk},
		"sk": &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

func number(n int64) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

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
	if i.references() {
		m["gsi1pk"] = &ddbtypes.AttributeValueMemberS{Value: i.TargetPK}
		m["gsi1sk"] = &ddbtypes.AttributeValueMemberS{Value: i.TargetSK}
	}
	if len(i.Names) > 0 {
		m["links"] = &ddbtypes.AttributeValueMemberSS{Value: i.Names}
	}
	return m
}

func unmarshal(raw map[string]ddbtypes.AttributeValue) (item, error) {
	i := item{}
	for name, field := range map[string]*string{"pk": &i.PK, "sk": &i.SK, "gsi1pk": &i.TargetPK, "gsi1sk": &i.TargetSK, "record": &i.Record, "owner": &i.Owner} {
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
	if v, ok := raw["links"].(*ddbtypes.AttributeValueMemberSS); ok {
		i.Names = v.Value
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
