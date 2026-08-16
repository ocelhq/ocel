package vars

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"golang.org/x/sync/errgroup"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

var ErrNotPublished = errors.New("vars: link not published")

var ErrUnreadableRecord = errors.New("vars: unreadable link record")

var ErrUnscopedGrant = errors.New("vars: unscoped grant")

var errTornPair = errors.New("vars: torn link pair")

type PublishedRecord struct {
	Link        *linksv1.Link
	Environment string
	Version     int64
	UpdatedAt   int64
}

func (r PublishedRecord) Name() string {
	return r.Link.GetName()
}

func (r PublishedRecord) Type() linksv1.LinkType {
	return naming.LinkTypeOf(r.Link)
}

func EncodeLink(link *linksv1.Link) ([]byte, error) {
	return protojson.Marshal(link)
}

func DecodeLink(raw []byte) (*linksv1.Link, error) {
	link := &linksv1.Link{}
	if err := protojson.Unmarshal(raw, link); err != nil {
		return nil, err
	}
	return link, nil
}

func redacted(link *linksv1.Link) *linksv1.Link {
	out := proto.Clone(link).(*linksv1.Link)
	m := out.ProtoReflect()
	if fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("properties")); fd != nil {
		m.Set(fd, protoreflect.ValueOfMessage(m.Get(fd).Message().New()))
	}
	return out
}

func LinkPartitionKey(slug, class, link string) string {
	return naming.LinkVarsKey(slug, class, link)
}

func linkCoordinate(slug, link, environment string) Coordinate {
	return Coordinate{Slug: slug, Link: link, Key: linkValueKey, Environment: environment}
}

func (c Coordinate) partition(class string) string {
	if c.Link != "" {
		return LinkPartitionKey(c.Slug, class, c.Link)
	}
	return PartitionKey(c.Slug, class)
}

func ValidateLinkTarget(slug, environment string) error {
	if slug == "" {
		return fmt.Errorf("a project slug is required")
	}
	if environment == ClassWideEnvironment {
		return fmt.Errorf(
			"%q is reserved: it names the pair that binds class-wide. Leave the environment off to publish there, which serves every preview including the ephemeral ones",
			ClassWideEnvironment)
	}
	for name, component := range map[string]string{"project slug": slug, "environment name": environment} {
		if strings.Contains(component, delimiter) {
			return fmt.Errorf("%s %q may not contain %q", name, component, delimiter)
		}
	}
	return nil
}

func ValidateLinkName(slug, environment, name string) error {
	if err := ValidateLinkTarget(slug, environment); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("a link name is required")
	}
	if strings.Contains(name, delimiter) {
		return fmt.Errorf("link name %q may not contain %q", name, delimiter)
	}
	return nil
}

type PublishResult struct {
	Published []string
	Pruned    int
}

const OwnerOcel = "OCEL"

func validateOwner(owner string) error {
	if owner == "" {
		return fmt.Errorf("a publisher name is required: it is what keeps one publisher from pruning another's records")
	}
	if strings.Contains(owner, delimiter) {
		return fmt.Errorf("publisher name %q may not contain %q", owner, delimiter)
	}
	return nil
}

func (s *Store) PublishRecords(ctx context.Context, slug, environment, owner string, records []*linksv1.Link) (PublishResult, error) {
	if err := validateOwner(owner); err != nil {
		return PublishResult{}, err
	}
	return s.publish(ctx, slug, owner, environment, records)
}

func (s *Store) publish(ctx context.Context, slug, owner, environment string, records []*linksv1.Link) (PublishResult, error) {
	published, err := publishedNames(slug, environment, records)
	if err != nil {
		return PublishResult{}, err
	}

	claimed, err := s.claims(ctx, slug)
	if err != nil {
		return PublishResult{}, err
	}
	if err := s.refuseClaimed(owner, published, claimed); err != nil {
		return PublishResult{}, err
	}
	elsewhere := claimedByOthers(owner, environment, claimed)

	sealed := make([][]byte, len(records))
	rows := make([][]byte, len(records))
	for i, r := range records {
		value, err := EncodeLink(r)
		if err != nil {
			return PublishResult{}, fmt.Errorf("render link %s: %w", r.GetName(), err)
		}
		if len(value) > MaxValueBytes {
			return PublishResult{}, fmt.Errorf("link %s is too large: %d bytes, limit %d", r.GetName(), len(value), MaxValueBytes)
		}
		if sealed[i], err = s.encrypt(ctx, linkCoordinate(slug, r.GetName(), environment).canonical(), string(value)); err != nil {
			return PublishResult{}, err
		}
		if rows[i], err = EncodeLink(redacted(r)); err != nil {
			return PublishResult{}, fmt.Errorf("render link %s's record: %w", r.GetName(), err)
		}
	}

	if len(published) > 0 {
		if _, err := s.writeLinkIndex(ctx, slug, owner, environment, published, elsewhere, false); err != nil {
			return PublishResult{}, err
		}
	}

	ts := s.now()
	group, writeCtx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, r := range records {
		group.Go(func() error {
			return s.writePair(writeCtx, linkCoordinate(slug, r.GetName(), environment), owner, sealed[i], rows[i], ts)
		})
	}
	if err := group.Wait(); err != nil {
		return PublishResult{}, err
	}

	pruned, err := s.writeLinkIndex(ctx, slug, owner, environment, published, elsewhere, true)
	if err != nil {
		return PublishResult{}, err
	}
	return PublishResult{Published: published, Pruned: pruned}, nil
}

func publishedNames(slug, environment string, records []*linksv1.Link) ([]string, error) {
	if err := ValidateLinkTarget(slug, environment); err != nil {
		return nil, err
	}

	published := make([]string, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, r := range records {
		if err := ValidateLinkName(slug, environment, r.GetName()); err != nil {
			return nil, err
		}
		if seen[r.GetName()] {
			return nil, fmt.Errorf("this deploy publishes link %s twice; the two publishes land on the same rows and whichever finishes last wins, so name one of them something else", r.GetName())
		}
		seen[r.GetName()] = true
		if err := Verify(r); err != nil {
			return nil, err
		}
		published = append(published, r.GetName())
	}
	slices.Sort(published)
	return published, nil
}

func (s *Store) writePair(ctx context.Context, c Coordinate, owner string, sealed, row []byte, ts int64) error {
	pk := c.partition(s.Class)
	record := &ddbtypes.Update{
		TableName:        aws.String(s.Table),
		Key:              pointKey(pk, recordSortKey(c.Environment)),
		UpdateExpression: aws.String("SET #record = :record, #ts = :ts, #owner = :owner ADD #version :one"),
		ExpressionAttributeNames: map[string]string{
			"#record": "record", "#ts": "ts", "#version": "version", "#owner": "owner",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":record": &ddbtypes.AttributeValueMemberS{Value: string(row)},
			":ts":     number(ts),
			":one":    number(1),
			":owner":  &ddbtypes.AttributeValueMemberS{Value: owner},
		},
	}
	if owner != OwnerOcel {
		record.ConditionExpression = aws.String("attribute_not_exists(#owner) OR #owner = :owner")
	}

	writes := []ddbtypes.TransactWriteItem{
		{Update: &ddbtypes.Update{
			TableName:        aws.String(s.Table),
			Key:              pointKey(pk, currentSortKey(c.canonical())),
			UpdateExpression: aws.String("SET #ciphertext = :ciphertext, #size = :size, #ts = :ts, #deleted = :deleted ADD #version :one"),
			ExpressionAttributeNames: map[string]string{
				"#ciphertext": "ciphertext", "#size": "size", "#ts": "ts", "#deleted": "deleted", "#version": "version",
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":ciphertext": &ddbtypes.AttributeValueMemberB{Value: sealed},
				":size":       number(int64(len(sealed))),
				":ts":         number(ts),
				":deleted":    &ddbtypes.AttributeValueMemberBOOL{Value: false},
				":one":        number(1),
			},
		}},
		{Update: record},
	}
	for range linkAttempts {
		_, err := s.Dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes})
		if err == nil {
			return nil
		}
		var cancelled *ddbtypes.TransactionCanceledException
		if !errors.As(err, &cancelled) {
			return fmt.Errorf("publish link %s: %w", c.Link, err)
		}
		if conditionFailed(cancelled) {
			return s.claimedBy(ctx, c, pk)
		}
	}

	return fmt.Errorf(
		"link %s could not be written %d attempts in a row, each cancelled by the store. "+
			"Either another deploy of the same environment is racing this one — run them one after the other — or the table is shedding load and this is worth retrying",
		c.Link, linkAttempts)
}

func conditionFailed(cancelled *ddbtypes.TransactionCanceledException) bool {
	for _, reason := range cancelled.CancellationReasons {
		if aws.ToString(reason.Code) == conditionalCheckFailed {
			return true
		}
	}
	return false
}

const conditionalCheckFailed = "ConditionalCheckFailed"

func (s *Store) claimedBy(ctx context.Context, c Coordinate, pk string) error {
	held, err := s.read(ctx, pk, recordSortKey(c.Environment))
	if err != nil {
		return err
	}
	return fmt.Errorf(
		"link %s in %s is already published by %s: one link name belongs to one publisher, and taking it would hand every app consuming that name another resource's values. "+
			"Give one of them another name: %w",
		c.Link, s.Class, describeOwner(ownerOf(held)), ErrClaimed)
}

const linkAttempts = 5

func (s *Store) writeLinkIndex(ctx context.Context, slug, owner, environment string, published []string, elsewhere map[string]bool, prune bool) (int, error) {
	pk, sk := PartitionKey(slug, s.Class), linkIndexSortKey(owner, environment)

	pruned := 0
	for range linkAttempts {
		previous, err := s.read(ctx, pk, sk)
		if err != nil {
			return pruned, err
		}

		owned := published
		if !prune {
			owned = union(previous.Names, published)
		}
		for _, stale := range previous.Names {
			if !prune || slices.Contains(published, stale) || elsewhere[stale] {
				continue
			}
			removed, err := s.dropLink(ctx, slug, owner, environment, stale)
			if err != nil {
				return pruned, err
			}
			pruned += removed
		}
		if prune && len(owned) == 0 && previous.Version > 0 {
			err = s.dropIndex(ctx, pk, sk, previous.Version)
			if err == nil {
				return pruned, nil
			}
			var lost *ddbtypes.TransactionCanceledException
			if !errors.As(err, &lost) {
				return pruned, err
			}
			continue
		}

		index := item{PK: pk, SK: sk, Version: previous.Version + 1, Names: owned, Ts: s.now()}
		_, err = s.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:                 aws.String(s.Table),
			Item:                      marshal(index),
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR #version = :seen"),
			ExpressionAttributeNames:  map[string]string{"#version": "version"},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":seen": number(previous.Version)},
		})
		if err == nil {
			return pruned, nil
		}
		var stale *ddbtypes.ConditionalCheckFailedException
		if !errors.As(err, &stale) {
			return pruned, fmt.Errorf("record %s's published links: %w", slug, err)
		}
	}

	return pruned, fmt.Errorf(
		"another deploy of %s kept rewriting its published links while this one tried to record its own, %d times over. "+
			"Two deploys of the same environment are racing; run them one after the other",
		slug, linkAttempts)
}

func (s *Store) dropIndex(ctx context.Context, pk, sk string, seen int64) error {
	_, err := s.Dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{{Delete: &ddbtypes.Delete{
			TableName:                 aws.String(s.Table),
			Key:                       pointKey(pk, sk),
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR #version = :seen"),
			ExpressionAttributeNames:  map[string]string{"#version": "version"},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":seen": number(seen)},
		}}},
	})
	return err
}

func union(previous, published []string) []string {
	out := slices.Clone(published)
	for _, name := range previous {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Store) PublishedNames(ctx context.Context, slug, class, environment string) ([]string, error) {
	if slug == "" {
		return nil, fmt.Errorf("a project slug is required")
	}
	indexes, err := s.queryConsistent(ctx, PartitionKey(slug, class), linkIndexPrefix)
	if err != nil {
		return nil, err
	}

	names := map[string]bool{}
	for _, index := range indexes {
		if _, at, ok := parseLinkIndexSortKey(index.SK); !ok || !bindsTo(at, environment) {
			continue
		}
		for _, name := range index.Names {
			names[name] = true
		}
	}
	return slices.Sorted(maps.Keys(names)), nil
}

func bindsTo(at, environment string) bool {
	return at == canonicalEnvironment(environment) || at == ClassWideEnvironment
}

func (s *Store) dropLink(ctx context.Context, slug, owner, environment, link string) (int, error) {
	pk := LinkPartitionKey(slug, s.Class, link)
	stored, err := s.queryConsistent(ctx, pk, "")
	if err != nil {
		return 0, err
	}

	for _, i := range stored {
		if i.SK == recordSortKey(environment) && ownerOf(i) != owner {
			return 0, nil
		}
	}

	return s.deleteRows(ctx, pk, stored, environment, link)
}

func (s *Store) deleteAll(ctx context.Context, keys []map[string]ddbtypes.AttributeValue, what string) error {
	for start := 0; start < len(keys); start += purgeBatch {
		batch := keys[start:min(start+purgeBatch, len(keys))]
		writes := make([]ddbtypes.TransactWriteItem, 0, len(batch))
		for _, key := range batch {
			writes = append(writes, ddbtypes.TransactWriteItem{Delete: &ddbtypes.Delete{
				TableName: aws.String(s.Table),
				Key:       key,
			}})
		}
		if _, err := s.Dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes}); err != nil {
			return fmt.Errorf("remove %s's stored values: %w", what, err)
		}
	}
	return nil
}

func (s *Store) ResolveRecords(ctx context.Context, slug, environment string, names []string) ([]PublishedRecord, error) {
	if slug == "" {
		return nil, fmt.Errorf("a project slug is required")
	}
	if len(names) == 0 {
		return nil, nil
	}

	out := make([]PublishedRecord, len(names))
	group, readCtx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, name := range names {
		group.Go(func() error {
			resolved, err := s.resolveRecord(readCtx, slug, environment, name)
			if err != nil {
				return err
			}
			out[i] = resolved
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) resolveRecord(ctx context.Context, slug, environment, name string) (PublishedRecord, error) {
	if err := ValidateLinkName(slug, environment, name); err != nil {
		return PublishedRecord{}, err
	}

	for range linkAttempts {
		resolved, err := s.readPair(ctx, slug, environment, name)
		if errors.Is(err, errTornPair) {
			continue
		}
		return resolved, err
	}

	return PublishedRecord{}, fmt.Errorf(
		"link %s's record and the value beside it came from different publishes, %d reads in a row. "+
			"A deploy is rewriting that link; nothing will be served half of one publish and half of another: %w",
		name, linkAttempts, ErrUnreadableRecord)
}

func (s *Store) readPair(ctx context.Context, slug, environment, name string) (PublishedRecord, error) {
	stored, err := s.queryConsistent(ctx, linkCoordinate(slug, name, environment).partition(s.Class), "")
	if err != nil {
		return PublishedRecord{}, err
	}
	rows := make(map[string]item, len(stored))
	for _, i := range stored {
		rows[i.SK] = i
	}

	for _, at := range shadowing(environment) {
		record, holds := rows[recordSortKey(at)]
		value, beside := rows[currentSortKey(linkCoordinate(slug, name, at).canonical())]
		beside = beside && !value.Deleted
		if !holds && !beside {
			continue
		}
		if !holds || !beside || record.Version != value.Version {
			return PublishedRecord{}, errTornPair
		}
		return s.openPair(ctx, linkCoordinate(slug, name, at), record, value)
	}

	return PublishedRecord{}, fmt.Errorf("link %s is not published to %s: %w", name, describeEnvironment(environment), ErrNotPublished)
}

func shadowing(environment string) []string {
	if environment == "" {
		return []string{""}
	}
	return []string{environment, ""}
}

func describeEnvironment(environment string) string {
	if environment == "" {
		return "the class"
	}
	return environment
}

func (s *Store) openPair(ctx context.Context, c Coordinate, record, value item) (PublishedRecord, error) {
	row, err := DecodeLink([]byte(record.Record))
	if err != nil {
		return PublishedRecord{}, fmt.Errorf("read link %s's record: %v: %w", c.Link, err, ErrUnreadableRecord)
	}

	plaintext, err := s.decrypt(ctx, c.canonical(), value.Ciphertext)
	if err != nil {
		return PublishedRecord{}, fmt.Errorf("open link %s's value: %v: %w", c.Link, err, ErrUnreadableRecord)
	}
	link, err := DecodeLink([]byte(plaintext))
	if err != nil {
		return PublishedRecord{}, fmt.Errorf("read link %s's value: %w", c.Link, ErrUnreadableRecord)
	}
	if link.GetName() != c.Link || naming.LinkTypeOf(link) != naming.LinkTypeOf(row) {
		return PublishedRecord{}, fmt.Errorf("link %s's record and the value beside it describe different links: %w", c.Link, ErrUnreadableRecord)
	}

	resolved := PublishedRecord{
		Link:        link,
		Environment: c.Environment,
		Version:     record.Version,
		UpdatedAt:   record.Ts,
	}
	if err := Verify(link); err != nil {
		return PublishedRecord{}, err
	}
	return resolved, nil
}

func Verify(link *linksv1.Link) error {
	if link.GetName() == "" {
		return fmt.Errorf("a link carries no name; the name is what a consuming app binds to: %w", ErrUnreadableRecord)
	}
	if naming.LinkTypeOf(link) == linksv1.LinkType_LINK_TYPE_UNSPECIFIED {
		return fmt.Errorf("link %s carries no properties, so it has no type a consumer can resolve it against: %w", link.GetName(), ErrUnreadableRecord)
	}
	for _, g := range link.GetGrants() {
		if len(g.GetActions()) == 0 || slices.ContainsFunc(g.GetActions(), UnscopedAction) {
			return fmt.Errorf("link %s grants %v over %v: an action naming a whole service reaches past the resource it links: %w",
				link.GetName(), g.GetActions(), g.GetResources(), ErrUnscopedGrant)
		}
		if len(g.GetResources()) == 0 || slices.ContainsFunc(g.GetResources(), UnscopedResource) {
			return fmt.Errorf("link %s grants %v over %v: an app receives permissions for the resource it links and nothing else: %w",
				link.GetName(), g.GetActions(), g.GetResources(), ErrUnscopedGrant)
		}
	}
	return nil
}

const grantWildcard = "*"

func UnscopedAction(action string) bool {
	service, verb, scoped := strings.Cut(action, ":")
	if !scoped {
		return action == grantWildcard
	}
	return verb == grantWildcard || service == grantWildcard
}

func UnscopedResource(resource string) bool {
	return resource == grantWildcard
}

func (s *Store) purgeLinks(ctx context.Context, slug string) error {
	indexes, err := s.queryConsistent(ctx, PartitionKey(slug, s.Class), linkIndexPrefix)
	if err != nil {
		return err
	}

	links := map[string]bool{}
	for _, index := range indexes {
		for _, name := range index.Names {
			links[name] = true
		}
	}

	for _, link := range slices.Sorted(maps.Keys(links)) {
		pk := LinkPartitionKey(slug, s.Class, link)
		stored, err := s.queryConsistent(ctx, pk, "")
		if err != nil {
			return err
		}
		keys := make([]map[string]ddbtypes.AttributeValue, 0, len(stored))
		for _, i := range stored {
			keys = append(keys, pointKey(pk, i.SK))
		}
		if err := s.deleteAll(ctx, keys, link); err != nil {
			return err
		}
	}
	return nil
}
