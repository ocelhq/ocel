package deploy

import (
	"github.com/ocelhq/ocel/pkg/edge"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type WorkerValues struct {
	ImageOptimizerURL  string
	RevalidateQueueURL string
	AssetBucket        string
	Region             string
	EdgeAccessKeyID    string
	EdgeSecretKey      string
}

func (f WorkerValues) Bindings() cloudflare.OriginBindings {
	bindings := cloudflare.OriginBindings{Variables: map[string]string{}}
	for name, value := range map[string]string{
		edge.ImageOptimizerURLVar:  f.ImageOptimizerURL,
		edge.RevalidateQueueURLVar: f.RevalidateQueueURL,
	} {
		if value != "" {
			bindings.Variables[name] = value
		}
	}
	if f.EdgeAccessKeyID == "" || f.EdgeSecretKey == "" {
		return bindings
	}
	bindings.Variables[edge.EdgeAccessKeyIDVar] = f.EdgeAccessKeyID
	bindings.Secrets = map[string]string{edge.EdgeSecretKeyVar: f.EdgeSecretKey}
	if f.AssetBucket != "" && f.Region != "" {
		bindings.Variables[edge.AssetBucketVar] = f.AssetBucket
		bindings.Variables[edge.AWSRegionVar] = f.Region
	}
	return bindings
}
