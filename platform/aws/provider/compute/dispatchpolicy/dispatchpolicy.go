package dispatchpolicy

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
)

const (
	AssetReadName = "asset-read"

	TagImageOptimizer = "image-optimizer"
)

func RenderAssetRead(edgeBucket string, coord naming.Coordinate) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s*", edgeBucket, coord.StoragePrefix(naming.ObjectAssets)),
			},
		},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render asset read policy: %w", err)
	}
	return string(out), nil
}

func RenderInvoke(siblings []string, account string, optimizer bool) (string, error) {
	statements := []any{}
	if len(siblings) > 0 {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   functionurl.InvokerActions(),
			"Resource": siblings,
		})
	}
	if optimizer {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   functionurl.InvokerActions(),
			"Resource": fmt.Sprintf("arn:aws:lambda:*:%s:function:*", account),
			"Condition": map[string]any{
				"StringEquals": map[string]any{
					"aws:ResourceTag/" + component.TagComponent: TagImageOptimizer,
				},
			},
		})
	}
	if len(statements) == 0 {
		return "", nil
	}
	out, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render dispatch invoke policy: %w", err)
	}
	return string(out), nil
}
