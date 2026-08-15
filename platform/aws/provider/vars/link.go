package vars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
)

var ErrNotPublished = errors.New("vars: link not published")

var ErrUnreadableRecord = errors.New("vars: unreadable link record")

var ErrUnscopedGrant = errors.New("vars: unscoped grant")

var errTornPair = errors.New("vars: torn link pair")

type Grant struct {
	Actions   []string `json:"actions,omitempty"`
	Resources []string `json:"resources,omitempty"`
	Label     string   `json:"label,omitempty"`
}

type Record struct {
	Name       string
	Type       string
	Properties map[string]string
	Grants     []Grant
}

type PublishedRecord struct {
	Record
	Environment string
	Version     int64
	UpdatedAt   int64
}

type recordRow struct {
	Type   string  `json:"type"`
	Grants []Grant `json:"grants,omitempty"`
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

func (c Coordinate) validateDerived() error {
	if c.Slug == "" {
		return fmt.Errorf("a project slug is required")
	}
	if c.Link == "" {
		return fmt.Errorf("a link name is required")
	}
	if c.Folder != "" {
		return fmt.Errorf("a link value is delivered to every folder of the apps that use it; %q addresses nothing", c.Folder)
	}
	if c.Environment == classWideEnvironment {
		return fmt.Errorf("%q is reserved: it names the pair that binds class-wide", classWideEnvironment)
	}
	for name, component := range map[string]string{
		"project slug":     c.Slug,
		"link name":        c.Link,
		"environment name": c.Environment,
	} {
		if strings.Contains(component, delimiter) {
			return fmt.Errorf("%s %q may not contain %q", name, component, delimiter)
		}
	}
	return nil
}

func (s *Store) PublishRecords(ctx context.Context, slug, environment, owner string, records []Record) (int, error) {
	if slug == "" {
		return 0, fmt.Errorf("a project slug is required")
	}
	if err := validateOwner(owner); err != nil {
		return 0, err
	}

	sealed := make([][]byte, len(records))
	rows := make([][]byte, len(records))
	published := make([]string, 0, len(records))
	seen := make(map[string]bool, len(records))
	for i, r := range records {
		c := linkCoordinate(slug, r.Name, environment)
		if err := c.validateDerived(); err != nil {
			return 0, err
		}
		if seen[r.Name] {
			return 0, fmt.Errorf("this deploy publishes link %s twice; the two publishes land on the same rows and whichever finishes last wins, so name one of them something else", r.Name)
		}
		seen[r.Name] = true
		if r.Type == "" {
			return 0, fmt.Errorf("link %s carries no type token; a consumer has nothing to resolve it against", r.Name)
		}
		if err := r.verify(); err != nil {
			return 0, err
		}
		bag, err := json.Marshal(r.bag())
		if err != nil {
			return 0, fmt.Errorf("render link %s's properties: %w", r.Name, err)
		}
		if len(bag) > MaxValueBytes {
			return 0, fmt.Errorf("properties of link %s are too large: %d bytes, limit %d", r.Name, len(bag), MaxValueBytes)
		}
		if sealed[i], err = s.encrypt(ctx, c.canonical(), string(bag)); err != nil {
			return 0, err
		}
		if rows[i], err = json.Marshal(recordRow{Type: r.Type, Grants: r.Grants}); err != nil {
			return 0, fmt.Errorf("render link %s's record: %w", r.Name, err)
		}
		published = append(published, r.Name)
	}
	slices.Sort(published)

	ts := s.now()
	group, writeCtx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, r := range records {
		group.Go(func() error {
			return s.writePair(writeCtx, linkCoordinate(slug, r.Name, environment), sealed[i], rows[i], ts)
		})
	}
	if err := group.Wait(); err != nil {
		return 0, err
	}

	return s.reconcileLinkIndex(ctx, slug, environment, owner, published)
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

func (r Record) bag() map[string]string {
	if r.Properties == nil {
		return map[string]string{}
	}
	return r.Properties
}

func (s *Store) writePair(ctx context.Context, c Coordinate, sealed, row []byte, ts int64) error {
	pk := c.partition(s.Class)
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
		{Update: &ddbtypes.Update{
			TableName:        aws.String(s.Table),
			Key:              pointKey(pk, recordSortKey(c.Environment)),
			UpdateExpression: aws.String("SET #record = :record, #ts = :ts ADD #version :one"),
			ExpressionAttributeNames: map[string]string{
				"#record": "record", "#ts": "ts", "#version": "version",
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":record": &ddbtypes.AttributeValueMemberS{Value: string(row)},
				":ts":     number(ts),
				":one":    number(1),
			},
		}},
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
	}

	return fmt.Errorf(
		"another deploy kept overwriting link %s while this one published it, %d times over. "+
			"Two deploys of the same environment are racing; run them one after the other",
		c.Link, linkAttempts)
}

const linkAttempts = 5

func (s *Store) reconcileLinkIndex(ctx context.Context, slug, environment, owner string, published []string) (int, error) {
	pk, sk := PartitionKey(slug, s.Class), linkIndexSortKey(owner, environment)

	claimed, err := s.claimedByOthers(ctx, pk, owner, environment)
	if err != nil {
		return 0, err
	}

	pruned := 0
	for range linkAttempts {
		previous, err := s.read(ctx, pk, sk)
		if err != nil {
			return pruned, err
		}

		for _, stale := range previous.Names {
			if slices.Contains(published, stale) || claimed[stale] {
				continue
			}
			removed, err := s.dropLink(ctx, slug, environment, stale)
			if err != nil {
				return pruned, err
			}
			pruned += removed
		}

		index := item{PK: pk, SK: sk, Version: previous.Version + 1, Names: published, Ts: s.now()}
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

func (s *Store) claimedByOthers(ctx context.Context, pk, owner, environment string) (map[string]bool, error) {
	indexes, err := s.queryConsistent(ctx, pk, linkIndexPrefix)
	if err != nil {
		return nil, err
	}

	claimed := map[string]bool{}
	for _, index := range indexes {
		by, at, ok := parseLinkIndexSortKey(index.SK)
		if !ok || by == owner || at != canonicalEnvironment(environment) {
			continue
		}
		for _, name := range index.Names {
			claimed[name] = true
		}
	}
	return claimed, nil
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
	return at == canonicalEnvironment(environment) || at == classWideEnvironment
}

func (s *Store) dropLink(ctx context.Context, slug, environment, link string) (int, error) {
	pk := LinkPartitionKey(slug, s.Class, link)
	stored, err := s.query(ctx, pk, "", true)
	if err != nil {
		return 0, err
	}

	suffix := delimiter + tokenEnv + delimiter + canonicalEnvironment(environment)
	var keys []map[string]ddbtypes.AttributeValue
	for _, i := range stored {
		if strings.HasSuffix(i.SK, suffix) {
			keys = append(keys, pointKey(pk, i.SK))
		}
	}
	return len(keys), s.deleteAll(ctx, keys, link)
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
	c := linkCoordinate(slug, name, environment)
	if err := c.validateDerived(); err != nil {
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
	var row recordRow
	if err := json.Unmarshal([]byte(record.Record), &row); err != nil {
		return PublishedRecord{}, fmt.Errorf("read link %s's record: %v: %w", c.Link, err, ErrUnreadableRecord)
	}

	plaintext, err := s.decrypt(ctx, c.canonical(), value.Ciphertext)
	if err != nil {
		return PublishedRecord{}, fmt.Errorf("open link %s's value: %v: %w", c.Link, err, ErrUnreadableRecord)
	}
	properties := map[string]string{}
	if err := json.Unmarshal([]byte(plaintext), &properties); err != nil {
		return PublishedRecord{}, fmt.Errorf("read link %s's properties: %v: %w", c.Link, err, ErrUnreadableRecord)
	}

	resolved := PublishedRecord{
		Record:      Record{Name: c.Link, Type: row.Type, Properties: properties, Grants: row.Grants},
		Environment: c.Environment,
		Version:     record.Version,
		UpdatedAt:   record.Ts,
	}
	if err := resolved.verify(); err != nil {
		return PublishedRecord{}, err
	}
	return resolved, nil
}

func (r Record) verify() error {
	if r.Type == "" {
		return fmt.Errorf("link %s carries no type token: %w", r.Name, ErrUnreadableRecord)
	}
	for _, g := range r.Grants {
		if len(g.Actions) == 0 || slices.ContainsFunc(g.Actions, UnscopedAction) {
			return fmt.Errorf("link %s grants %v over %v: an action naming a whole service reaches past the resource it links: %w",
				r.Name, g.Actions, g.Resources, ErrUnscopedGrant)
		}
		if len(g.Resources) == 0 || slices.ContainsFunc(g.Resources, UnscopedResource) {
			return fmt.Errorf("link %s grants %v over %v: an app receives permissions for the resource it links and nothing else: %w",
				r.Name, g.Actions, g.Resources, ErrUnscopedGrant)
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
	indexes, err := s.query(ctx, PartitionKey(slug, s.Class), linkIndexPrefix, true)
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
		stored, err := s.query(ctx, pk, "", true)
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
