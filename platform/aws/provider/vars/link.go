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

	"github.com/ocelhq/ocel/pkg/naming"
)

type LinkValue struct {
	Link  string
	Key   string
	Value string
}

func LinkPartitionKey(slug, class, link string) string {
	return naming.LinkVarsKey(slug, class, link)
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
	if c.Key == "" {
		return fmt.Errorf("a variable name is required")
	}
	if c.Folder != "" {
		return fmt.Errorf("a link value is delivered to every folder of the apps that use it; %q addresses nothing", c.Folder)
	}
	if c.Environment == classWideEnvironment {
		return fmt.Errorf("%q is reserved: it names the value that binds class-wide", classWideEnvironment)
	}
	for name, component := range map[string]string{
		"project slug":     c.Slug,
		"link name":        c.Link,
		"variable name":    c.Key,
		"environment name": c.Environment,
	} {
		if strings.Contains(component, delimiter) {
			return fmt.Errorf("%s %q may not contain %q", name, component, delimiter)
		}
	}
	return nil
}

func (s *Store) PublishLinks(ctx context.Context, slug, environment string, links []LinkValue) (int, error) {
	if slug == "" {
		return 0, fmt.Errorf("a project slug is required")
	}

	published := make([]string, 0, len(links))
	for _, l := range links {
		c := Coordinate{Slug: slug, Link: l.Link, Key: l.Key, Environment: environment}
		if err := c.validateDerived(); err != nil {
			return 0, err
		}
		if len(l.Value) > MaxValueBytes {
			return 0, fmt.Errorf("value for link %s is too large: %d bytes, limit %d", l.Link, len(l.Value), MaxValueBytes)
		}
		published = append(published, l.Link)
	}
	slices.Sort(published)
	published = slices.Compact(published)

	group, writeCtx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for _, l := range links {
		group.Go(func() error {
			return s.putLink(writeCtx, Coordinate{Slug: slug, Link: l.Link, Key: l.Key, Environment: environment}, l.Value)
		})
	}
	if err := group.Wait(); err != nil {
		return 0, err
	}

	return s.reconcileLinkIndex(ctx, slug, environment, published)
}

func (s *Store) putLink(ctx context.Context, c Coordinate, plaintext string) error {
	key := c.canonical()
	ciphertext, err := s.encrypt(ctx, key, plaintext)
	if err != nil {
		return err
	}
	stored := item{
		PK:         c.partition(s.Class),
		SK:         currentSortKey(key),
		Version:    1,
		Ciphertext: ciphertext,
		Size:       int64(len(plaintext)),
		Ts:         s.now(),
	}
	if _, err := s.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.Table),
		Item:      marshal(stored),
	}); err != nil {
		return fmt.Errorf("write link %s: %w", c.Link, err)
	}
	return nil
}

const linkIndexAttempts = 5

func (s *Store) reconcileLinkIndex(ctx context.Context, slug, environment string, published []string) (int, error) {
	pk, sk := PartitionKey(slug, s.Class), linkIndexSortKey(environment)

	pruned := 0
	for range linkIndexAttempts {
		previous, err := s.read(ctx, pk, sk)
		if err != nil {
			return pruned, err
		}

		for _, stale := range previous.Names {
			if slices.Contains(published, stale) {
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
		slug, linkIndexAttempts)
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

func (s *Store) RevealLinks(ctx context.Context, slug string, cells []Coordinate) ([]Value, error) {
	if slug == "" {
		return nil, fmt.Errorf("a project slug is required")
	}

	wanted := map[string]Coordinate{}
	partitions := map[string]bool{}
	for _, c := range cells {
		c.Slug = slug
		if err := c.validateDerived(); err != nil {
			return nil, err
		}
		wanted[cellKey(c.partition(s.Class), currentSortKey(c.canonical()))] = c
		partitions[c.partition(s.Class)] = true
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	pks := slices.Sorted(maps.Keys(partitions))
	pages := make([][]item, len(pks))
	group, queryCtx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, pk := range pks {
		group.Go(func() error {
			stored, err := s.query(queryCtx, pk, currentPrefix, true)
			if err != nil {
				return err
			}
			pages[i] = stored
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	var found []item
	var at []Coordinate
	for _, page := range pages {
		for _, stored := range page {
			c, ok := wanted[cellKey(stored.PK, stored.SK)]
			if !ok || stored.Deleted {
				continue
			}
			found = append(found, stored)
			at = append(at, c)
		}
	}

	plaintexts := make([]string, len(found))
	group, decryptCtx := errgroup.WithContext(ctx)
	group.SetLimit(revealConcurrency)
	for i, stored := range found {
		group.Go(func() error {
			plaintext, err := s.decrypt(decryptCtx, at[i].canonical(), stored.Ciphertext)
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
		values[i] = Value{
			Metadata:  Metadata{Coordinate: at[i], Version: stored.Version, UpdatedAt: stored.Ts, Size: stored.Size},
			Plaintext: plaintexts[i],
		}
	}
	return values, nil
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
