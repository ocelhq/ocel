package aws

type Options struct {
	Region       string            `json:"region,omitempty" examples:"af-south-1,ap-east-1,ap-east-2,ap-northeast-1,ap-northeast-2,ap-northeast-3,ap-south-1,ap-south-2,ap-southeast-1,ap-southeast-2,ap-southeast-3,ap-southeast-4,ap-southeast-5,ap-southeast-6,ap-southeast-7,ca-central-1,ca-west-1,eu-central-1,eu-central-2,eu-north-1,eu-south-1,eu-south-2,eu-west-1,eu-west-2,eu-west-3,il-central-1,me-central-1,me-south-1,mx-central-1,sa-east-1,us-east-1,us-east-2,us-west-1,us-west-2" doc:"The AWS region to deploy into, such as us-east-1: a region, never an availability zone such as us-east-1a. Omit it and ocel reads AWS_REGION, AWS_DEFAULT_REGION, then the profile."`
	VariablesKey string            `json:"variablesKey,omitempty" pattern:"^arn:aws:kms:" doc:"ARN of a KMS key to encrypt this account's variables under. Omit it and ocel bootstrap makes a key ocel owns, once a project here first seals a value."`
	Certificates map[string]string `json:"certificates,omitempty" doc:"Certificates to serve a hostname with, keyed by hostname, valued by the ARN of an already-issued ACM certificate. A hostname left off gets a certificate ocel requests, validates and deletes again."`
	Logs         LogOptions        `json:"logs,omitzero" doc:"How ocel logs --tail reads this account's logs."`
	Bytecode     BytecodeOptions   `json:"bytecode,omitzero" doc:"What a deploy does with the V8 compile cache it warms for each node function."`
}

type BytecodeOptions struct {
	Embed bool `json:"embed,omitempty" doc:"Fold each function's warmed compile cache into its own code package, so a cold start loads it with the code rather than fetching it from S3. Off, a cold start fetches it."`
}

type LogOptions struct {
	LiveTail bool `json:"liveTail,omitempty" doc:"Tail through CloudWatch Live Tail: lower latency and no sampling gaps between polls, billed per session-minute past the free tier and capped at 15 sessions per account. Off, ocel logs --tail polls FilterLogEvents."`
}
