package deploy

import (
	"github.com/ocelhq/ocel/pkg/edge"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type WorkerFacts struct {
	Region             string
	StateTable         string
	ImageOptimizerURL  string
	RevalidateQueueURL string
	EdgeAccessKeyID    string
	EdgeSecretKey      string
}

func (f WorkerFacts) Bindings() cloudflare.OriginBindings {
	bindings := cloudflare.OriginBindings{Variables: map[string]string{}}
	for name, value := range map[string]string{
		edge.AWSRegionVar:          f.Region,
		edge.StateTableVar:         f.StateTable,
		edge.ImageOptimizerURLVar:  f.ImageOptimizerURL,
		edge.RevalidateQueueURLVar: f.RevalidateQueueURL,
	} {
		if value != "" {
			bindings.Variables[name] = value
		}
	}
	if f.EdgeAccessKeyID != "" && f.EdgeSecretKey != "" {
		bindings.Variables[edge.EdgeAccessKeyIDVar] = f.EdgeAccessKeyID
		bindings.Secrets = map[string]string{edge.EdgeSecretKeyVar: f.EdgeSecretKey}
	}
	return bindings
}
