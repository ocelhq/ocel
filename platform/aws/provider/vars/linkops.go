package vars

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type LinkSummary struct {
	Name       string
	Type       linksv1.LinkType
	Source     string
	Owner      string
	Version    int64
	Properties []naming.PropertyShape
}

func (s *Store) SetLink(ctx context.Context, slug, owner, environment string, link *linksv1.Link) (int64, error) {
	if err := validatePublisher(owner); err != nil {
		return 0, err
	}
	if err := refuseUnsourced(owner, link); err != nil {
		return 0, err
	}
	published, err := publishedNames(slug, environment, []*linksv1.Link{link})
	if err != nil {
		return 0, err
	}

	claimed, err := s.claims(ctx, slug)
	if err != nil {
		return 0, err
	}
	if by, taken := otherOwner(claimed[link.GetName()], owner); taken {
		return 0, fmt.Errorf(
			"link %s in %s is already published by %s, and %s is asking to write it: one link name belongs to one publisher, and taking it would hand every app consuming that name another resource's values. "+
				"Give one of them another name, or remove the published one first: %w",
			link.GetName(), s.Class, describeOwner(by), describeOwner(owner), ErrClaimed)
	}

	value, err := EncodeLink(link)
	if err != nil {
		return 0, fmt.Errorf("render link %s: %w", link.GetName(), err)
	}
	if len(value) > MaxValueBytes {
		return 0, fmt.Errorf("link %s is too large: %d bytes, limit %d", link.GetName(), len(value), MaxValueBytes)
	}
	c := linkCoordinate(slug, link.GetName(), environment)
	sealed, err := s.encrypt(ctx, c.canonical(), string(value))
	if err != nil {
		return 0, err
	}
	row, err := EncodeLink(redacted(link))
	if err != nil {
		return 0, fmt.Errorf("render link %s's record: %w", link.GetName(), err)
	}
	shapes, err := encodeShapes(link)
	if err != nil {
		return 0, fmt.Errorf("render link %s's shape: %w", link.GetName(), err)
	}

	if _, err := s.writeLinkIndex(ctx, slug, owner, environment, published, nil, false); err != nil {
		return 0, err
	}
	if err := s.writePair(ctx, c, owner, sealed, row, shapes, s.now()); err != nil {
		return 0, err
	}

	written, err := s.read(ctx, c.partition(s.Class), recordSortKey(environment))
	if err != nil {
		return 0, err
	}
	return written.Version, nil
}

func (s *Store) RemoveLink(ctx context.Context, slug, environment, name string) (bool, error) {
	if err := ValidateLinkName(slug, environment, name); err != nil {
		return false, err
	}

	claimed, err := s.claims(ctx, slug)
	if err != nil {
		return false, err
	}
	at := canonicalEnvironment(environment)
	var owners []string
	for _, c := range claimed[name] {
		if c.environment == at && !slices.Contains(owners, c.owner) {
			owners = append(owners, c.owner)
		}
	}
	if len(owners) == 0 {
		return false, nil
	}
	slices.Sort(owners)

	for _, owner := range owners {
		if err := s.unindexLink(ctx, slug, owner, environment, name); err != nil {
			return false, err
		}
	}
	if _, err := s.dropRows(ctx, slug, environment, name); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) unindexLink(ctx context.Context, slug, owner, environment, name string) error {
	pk, sk := PartitionKey(slug, s.Class), linkIndexSortKey(owner, environment)

	for range linkAttempts {
		previous, err := s.read(ctx, pk, sk)
		if err != nil {
			return err
		}
		kept := slices.DeleteFunc(slices.Clone(previous.Names), func(n string) bool { return n == name })
		if len(kept) == len(previous.Names) {
			return nil
		}

		if len(kept) == 0 && previous.Version > 0 {
			err = s.dropIndex(ctx, pk, sk, previous.Version)
			if err == nil {
				return nil
			}
			var lost *ddbtypes.TransactionCanceledException
			if !errors.As(err, &lost) {
				return err
			}
			continue
		}

		index := item{PK: pk, SK: sk, Version: previous.Version + 1, Names: kept, Ts: s.now()}
		_, err = s.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:                 aws.String(s.Table),
			Item:                      marshal(index),
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR #version = :seen"),
			ExpressionAttributeNames:  map[string]string{"#version": "version"},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":seen": number(previous.Version)},
		})
		if err == nil {
			return nil
		}
		var stale *ddbtypes.ConditionalCheckFailedException
		if !errors.As(err, &stale) {
			return fmt.Errorf("record %s's published links: %w", slug, err)
		}
	}

	return fmt.Errorf(
		"a deploy of %s kept rewriting its published links while this one tried to take %s out of them, %d times over. "+
			"A deploy of the same environment is racing this removal; run them one after the other",
		slug, name, linkAttempts)
}

func (s *Store) dropRows(ctx context.Context, slug, environment, link string) (int, error) {
	pk := LinkPartitionKey(slug, s.Class, link)
	stored, err := s.queryConsistent(ctx, pk, "")
	if err != nil {
		return 0, err
	}
	return s.deleteRows(ctx, pk, stored, environment, link)
}

func (s *Store) deleteRows(ctx context.Context, pk string, stored []item, environment, link string) (int, error) {
	suffix := delimiter + tokenEnv + delimiter + canonicalEnvironment(environment)
	var keys []map[string]ddbtypes.AttributeValue
	for _, i := range stored {
		if strings.HasSuffix(i.SK, suffix) {
			keys = append(keys, pointKey(pk, i.SK))
		}
	}
	return len(keys), s.deleteAll(ctx, keys, link)
}

func (s *Store) ListLinks(ctx context.Context, slug, environment string) ([]LinkSummary, error) {
	names, err := s.PublishedNames(ctx, slug, s.Class, environment)
	if err != nil {
		return nil, err
	}

	out := make([]LinkSummary, 0, len(names))
	for _, name := range names {
		stored, err := s.queryConsistent(ctx, LinkPartitionKey(slug, s.Class, name), recordPrefix)
		if err != nil {
			return nil, err
		}
		rows := make(map[string]item, len(stored))
		for _, i := range stored {
			rows[i.SK] = i
		}

		for _, at := range shadowing(environment) {
			record, holds := rows[recordSortKey(at)]
			if !holds {
				continue
			}
			link, err := DecodeLink([]byte(record.Record))
			if err != nil {
				return nil, fmt.Errorf("read link %s's record: %v: %w", name, err, ErrUnreadableRecord)
			}
			shapes, err := decodeShapes(record.Shapes)
			if err != nil {
				return nil, fmt.Errorf("read link %s's shape: %v: %w", name, err, ErrUnreadableRecord)
			}
			out = append(out, LinkSummary{
				Name:       name,
				Type:       naming.LinkTypeOf(link),
				Source:     link.GetSource(),
				Owner:      ownerOf(record),
				Version:    record.Version,
				Properties: shapes,
			})
			break
		}
	}
	slices.SortFunc(out, func(a, b LinkSummary) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
