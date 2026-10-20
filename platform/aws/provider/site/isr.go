package site

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
)

type ISR struct {
	Coord     naming.Coordinate
	Namespace string

	EdgeBucket  string
	CacheBucket string
	Table       string
	TableARN    string

	PurgeSecret string
}

func (c ISR) TagNamespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	if c.Coord.Project == "" || c.Coord.Env == "" || c.Coord.App == "" || c.Coord.Release.IsZero() {
		return ""
	}
	return naming.ISRTagPrefix(c.Coord.Project, c.Coord.Stack())
}

func (c ISR) Env() map[string]string {
	env := map[string]string{
		"OCEL_EDGE_BUCKET":        c.EdgeBucket,
		"OCEL_CACHE_BUCKET":       c.CacheBucket,
		"OCEL_STORAGE_COORDINATE": c.Coord.StorageCoordinate(),
		"OCEL_STATE_TABLE":        c.Table,
		"OCEL_ISR_TAG_NAMESPACE":  c.TagNamespace(),
	}
	return env
}

func (c ISR) Policy() (string, error) {
	namespace := c.TagNamespace()
	if namespace == "" {
		return "", fmt.Errorf("the isr cache in %s and %s has no coordinate, so neither its objects nor its tag items can be scoped to this app", c.EdgeBucket, c.CacheBucket)
	}
	objects := make([]string, len(naming.ISRObjectKinds))
	for i, kind := range naming.ISRObjectKinds {
		objects[i] = c.objectARN(kind)
	}
	statements := []any{
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"s3:GetObject", "s3:PutObject"},
			"Resource": objects,
		},
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"dynamodb:UpdateItem"},
			"Resource": c.TableARN,
			"Condition": map[string]any{
				"ForAllValues:StringLike": map[string]any{
					"dynamodb:LeadingKeys": []string{namespace + "*"},
				},
			},
		},
	}

	doc := map[string]any{"Version": "2012-10-17", "Statement": statements}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render isr policy: %w", err)
	}
	return string(out), nil
}

func (c ISR) bucketOf(kind naming.ObjectKind) string {
	if kind.Class() == naming.StorageEdge {
		return c.EdgeBucket
	}
	return c.CacheBucket
}

func (c ISR) objectARN(kind naming.ObjectKind) string {
	return fmt.Sprintf("arn:aws:s3:::%s/%s*", c.bucketOf(kind), c.Coord.StoragePrefix(kind))
}
