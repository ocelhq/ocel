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

// ErrIsReference reports that a cell holds a reference rather than a value of
// its own, so what a caller asked to change is not there to change. Editing
// happens where the value is set; this is what makes that one place rather than
// several.
var ErrIsReference = errors.New("that cell is a reference, and a reference has no value of its own")

// ErrWouldDeepen reports a chain of references longer than one hop: on a write,
// whichever end it was attempted from — pointing at a cell that is already a
// pointer, pointing a cell that others already point at, or pointing a cell at
// itself — and on a read, the chain a lagging index let through anyway. Depth
// one is what makes resolution a single follow, so a read that finds it does
// not hold refuses rather than following again.
var ErrWouldDeepen = errors.New("a reference may only point at a value, never at another reference")

// SetReference points a cell at a value owned elsewhere, so a credential is set
// once and consumed in many places instead of copied into each. The cell holds
// the target's address and nothing else: resolution is a read-time follow, so a
// consumer never holds a stale copy and an edit at the source needs no
// synchronisation to reach it.
//
// The target is a class-wide value, always. A named environment is one
// consumer's own axis, and the consumer's environment names mean nothing in the
// target project's namespace — a reference that resolved against one would be
// reading a value whose name it cannot know is the same name.
//
// Depth is refused on the write, from both ends, by reading the target and
// asking the reverse index what already points here. The target read is
// consistent, so a loop cannot be closed at all. The index read is a GSI and is
// not, so two writes moments apart can leave a chain this one meant to refuse —
// which is why the read path refuses a second hop rather than assuming one.
//
// The target need not hold a value yet. Whether it does is not an invariant
// this refusal could hold anyway — deleting a value out from under its
// consumers is deliberately allowed — so all a write-time existence check buys
// is an ordering constraint between the team that sets a credential and the
// team that consumes it, and a placeholder value typed in to get around it,
// which the discovery gate would then accept as the real one. A reference to
// nothing fails the read, loudly, naming the cell that holds nothing.
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

// References answers what points at a cell, so the blast radius of an edit is
// visible before the edit. It is the reverse index's only reader, and the
// reason the index exists: without one the question is a scan of every
// project's values in the class.
//
// The answer is the consumers' coordinates, sorted. It is the index's own
// answer, so it is as current as the index is: a reference written moments ago
// may not be in it yet, which is the same lag the write guard is bounded by.
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

// ReferencedProjects is every other project this one reads a value from,
// sorted. It is what a deploy grants its functions the partitions of: a
// reference is followed where it is read, so a function reading one reads the
// owner's rows, and a grant over this project alone would deny at runtime what
// the store accepted at write.
//
// It costs the one query List already makes, because a reference's target rides
// in the row rather than behind a second read.
func (s *Store) ReferencedProjects(ctx context.Context, slug string) ([]string, error) {
	held, err := s.List(ctx, slug)
	if err != nil {
		return nil, err
	}
	owners := map[string]bool{}
	for _, m := range held {
		if m.Target.Slug != "" && m.Target.Slug != slug {
			owners[m.Target.Slug] = true
		}
	}

	out := make([]string, 0, len(owners))
	for owner := range owners {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out, nil
}

// describeCoordinates names a set of cells in one phrase, sorted, so a refusal
// naming several reads the same way twice.
func describeCoordinates(cells []Coordinate) string {
	names := make([]string, 0, len(cells))
	for _, c := range cells {
		names = append(names, c.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
