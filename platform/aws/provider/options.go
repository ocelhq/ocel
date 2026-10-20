package aws

import "github.com/ocelhq/ocel/pkg/environment"

type Options struct {
	Region       string            `json:"region,omitempty" examples:"af-south-1,ap-east-1,ap-east-2,ap-northeast-1,ap-northeast-2,ap-northeast-3,ap-south-1,ap-south-2,ap-southeast-1,ap-southeast-2,ap-southeast-3,ap-southeast-4,ap-southeast-5,ap-southeast-6,ap-southeast-7,ca-central-1,ca-west-1,eu-central-1,eu-central-2,eu-north-1,eu-south-1,eu-south-2,eu-west-1,eu-west-2,eu-west-3,il-central-1,me-central-1,me-south-1,mx-central-1,sa-east-1,us-east-1,us-east-2,us-west-1,us-west-2" doc:"The AWS region to deploy into, such as us-east-1. Left off, read from AWS_REGION, AWS_DEFAULT_REGION or the profile."`
	VariablesKey string            `json:"variablesKey,omitempty" pattern:"^arn:aws:kms:" doc:"ARN of a KMS key to encrypt this account's variables with. Left off, ocel creates one."`
	Certificates map[string]string `json:"certificates,omitempty" doc:"ARNs of issued ACM certificates, keyed by hostname. A hostname left off gets a certificate ocel manages."`
	Logs         LogOptions        `json:"logs,omitzero" doc:"How ocel logs --tail reads this account's logs."`
	Bytecode     BytecodeOptions   `json:"bytecode,omitzero" doc:"What a deploy does with the V8 compile cache it warms for each node function."`
	Network      NetworkOptions    `json:"network,omitzero" doc:"The VPC ocel creates for each tier, which its container apps, VPC-attached functions, databases and caches run in."`
}

type NetworkOptions struct {
	Production TierNetworkOptions `json:"production,omitzero" doc:"The production tier's VPC."`
	Preview    TierNetworkOptions `json:"preview,omitzero" doc:"The preview tier's VPC."`
}

type TierNetworkOptions struct {
	CIDR string `json:"cidr,omitempty" pattern:"^[0-9]{1,3}(\\.[0-9]{1,3}){3}/(1[6-9]|2[0-4])$" doc:"The private IPv4 block of the tier's VPC, a /16 to a /24 inside 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16. Left off, a /16 from 10.16.0.0 to 10.255.0.0 picked from the namespace, preview's right after production's. Fixed once the VPC exists."`
}

func (n NetworkOptions) CIDRFor(tier environment.Tier) string {
	if tier == environment.TierProduction {
		return n.Production.CIDR
	}
	return n.Preview.CIDR
}

type BytecodeOptions struct {
	Cache bool `json:"cache,omitempty" doc:"Warm a V8 compile cache for each node function at deploy, so cold starts compile less. Left off, false."`
	Embed bool `json:"embed,omitempty" doc:"Bundle each function's compile cache into its code package instead of fetching it from S3. Needs cache."`
}

type LogOptions struct {
	LiveTail bool `json:"liveTail,omitempty" doc:"Tail logs through CloudWatch Live Tail instead of polling. Billed per session-minute past the free tier."`
}
