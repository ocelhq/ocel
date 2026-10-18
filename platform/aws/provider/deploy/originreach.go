package deploy

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	lambdaInvokePayloadBytes      = 6 << 20
	functionURLEventEnvelopeBytes = 64 << 10

	MaxImageRequestBytes = lambdaInvokePayloadBytes - functionURLEventEnvelopeBytes
)

const lambdaThrottleHeader = "x-amzn-errortype"

type OriginAccess struct {
	Region            string
	AssetBucket       string
	AccessKeyID       string
	SecretAccessKey   string
	ImageOptimizerURL string
}

func OriginReach(access OriginAccess) edge.OriginReach {
	reach := edge.OriginReach{
		Reach: edge.Reach{
			Kind:            edge.ReachSigV4,
			Service:         "lambda",
			Region:          access.Region,
			AccessKeyID:     access.AccessKeyID,
			SecretAccessKey: access.SecretAccessKey,
		},
		Throttle: edge.Throttle{Status: 429, Header: lambdaThrottleHeader},
	}
	if access.AssetBucket != "" {
		reach.Store = &edge.OriginStore{
			URL:             fmt.Sprintf("https://%s.s3.%s.amazonaws.com", access.AssetBucket, access.Region),
			Region:          access.Region,
			HoldsEntries:    true,
			AccessKeyID:     access.AccessKeyID,
			SecretAccessKey: access.SecretAccessKey,
		}
	}
	if access.ImageOptimizerURL != "" {
		reach.Images = &edge.OriginImages{OptimizerURL: access.ImageOptimizerURL, MaxRequestBytes: MaxImageRequestBytes}
	}
	return reach
}
