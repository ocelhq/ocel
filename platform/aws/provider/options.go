package aws

type Options struct {
	Region       string            `json:"region,omitempty" doc:"The AWS region to deploy into."`
	VariablesKey string            `json:"variablesKey,omitempty" pattern:"^arn:aws:kms:" doc:"ARN of a KMS key to encrypt this account's variables under. Omit it and ocel bootstrap --features variables-key makes a key ocel owns."`
	Certificates map[string]string `json:"certificates,omitempty" doc:"Certificates to serve a hostname with, keyed by hostname, valued by the ARN of an already-issued ACM certificate. A hostname left off gets a certificate ocel requests, validates and deletes again."`
	Logs         LogOptions        `json:"logs,omitzero" doc:"How ocel logs --tail reads this account's logs."`
}

type LogOptions struct {
	LiveTail bool `json:"liveTail,omitempty" doc:"Tail through CloudWatch Live Tail: lower latency and no sampling gaps between polls, billed per session-minute past the free tier and capped at 15 sessions per account. Off, ocel logs --tail polls FilterLogEvents."`
}
