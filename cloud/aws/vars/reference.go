package vars

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var ErrIsReference = errors.New("that cell is a reference, and a reference has no value of its own")

var ErrWouldDeepen = errors.New("a reference may only point at a value, never at another reference")

func (s *Store) SetReference(ctx context.Context, c, target Coordinate, expected *int64) (Metadata, error) {
	if err := c.validate(); err != nil {
		return Metadata{}, err
	}
	if err := target.validate(); err != nil {
		return Metadata{}, fmt.Errorf("reference target: %w", err)
	}
	if target.Environment != "" {
		return Metadata{}, fmt.Errorf("a reference resolves against %s's class-wide value; %q is an environment of the project holding the reference, and names nothing in the target's",
			target.Key, target.Environment)
	}
	if c.canonical() == target.canonical() {
		return Metadata{}, fmt.Errorf("%s would reference itself: %w", c, ErrWouldDeepen)
	}

	targetPK, targetSK := PartitionKey(target.Slug, s.Class), currentSortKey(target.canonical())
	pointedAt, err := s.read(ctx, targetPK, targetSK)
	if err != nil {
		return Metadata{}, err
	}
	if pointedAt.references() {
		return Metadata{}, fmt.Errorf("%s is itself a reference: %w", target, ErrWouldDeepen)
	}

	consumers, err := s.References(ctx, c)
	if err != nil {
		return Metadata{}, err
	}
	if len(consumers) > 0 {
		return Metadata{}, fmt.Errorf("%s is referenced by %s, which would then be reading a reference: %w", c, describeCoordinates(consumers), ErrWouldDeepen)
	}

	current, err := s.read(ctx, PartitionKey(c.Slug, s.Class), currentSortKey(c.canonical()))
	if err != nil {
		return Metadata{}, err
	}
	return s.commit(ctx, c, current, expected, item{TargetPK: targetPK, TargetSK: targetSK})
}

func (s *Store) References(ctx context.Context, target Coordinate) ([]Coordinate, error) {
	if err := target.validate(); err != nil {
		return nil, err
	}
	if target.Environment != "" {
		return nil, nil
	}

	values := map[string]ddbtypes.AttributeValue{
		":pk": &ddbtypes.AttributeValueMemberS{Value: PartitionKey(target.Slug, s.Class)},
		":sk": &ddbtypes.AttributeValueMemberS{Value: currentSortKey(target.canonical())},
	}

	var out []Coordinate
	var start map[string]ddbtypes.AttributeValue
	for {
		page, err := s.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.Table),
			IndexName:                 aws.String(IndexName),
			KeyConditionExpression:    aws.String("gsi1pk = :pk AND gsi1sk = :sk"),
			ExpressionAttributeValues: values,
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("read what references %s: %w", target, err)
		}
		for _, raw := range page.Items {
			stored, err := unmarshal(raw)
			if err != nil {
				return nil, err
			}
			slug, err := parsePartitionKey(stored.PK)
			if err != nil {
				return nil, err
			}
			c, err := parseCurrentSortKey(slug, stored.SK)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		if len(page.LastEvaluatedKey) == 0 {
			break
		}
		start = page.LastEvaluatedKey
	}

	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func (s *Store) ReferenceOwners(ctx context.Context, slug string) (map[Coordinate]string, error) {
	held, err := s.List(ctx, slug)
	if err != nil {
		return nil, err
	}
	owners := map[Coordinate]string{}
	for _, m := range held {
		if m.Target.Slug != "" && m.Target.Slug != slug {
			owners[m.Coordinate] = m.Target.Slug
		}
	}
	return owners, nil
}

func describeCoordinates(cells []Coordinate) string {
	names := make([]string, 0, len(cells))
	for _, c := range cells {
		names = append(names, c.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
