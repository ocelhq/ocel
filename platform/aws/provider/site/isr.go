package site

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
)

type ISR struct {
	Coord     naming.Coordinate
	Namespace string

	Buckets  naming.Buckets
	Table    string
	TableARN string

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
	return map[string]string{
		processenv.ISREnvVar:     "1",
		"OCEL_STATE_TABLE":       c.Table,
		"OCEL_ISR_TAG_NAMESPACE": c.TagNamespace(),
	}
}

func (c ISR) Policy() (string, error) {
	namespace := c.TagNamespace()
	if namespace == "" {
		return "", fmt.Errorf("the isr cache in %s and %s has no coordinate, so neither its objects nor its tag items can be scoped to this app", c.Buckets[naming.StorageEdge], c.Buckets[naming.StorageCache])
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

func (c ISR) objectARN(kind naming.ObjectKind) string {
	return fmt.Sprintf("arn:aws:s3:::%s/%s*", c.Buckets.Holding(kind), c.Coord.StoragePrefix(kind))
}
