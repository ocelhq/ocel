package provider

type Options struct {
	Region       string            `json:"region,omitempty" doc:"The AWS region to deploy into."`
	VarsKey      string            `json:"varsKey,omitempty" pattern:"^arn:aws:kms:" doc:"ARN of a KMS key to encrypt this account's variables under. Omit it and ocel bootstrap --features vars-key makes a key ocel owns."`
	Certificates map[string]string `json:"certificates,omitempty" doc:"Certificates to serve a hostname with, keyed by hostname, valued by the ARN of an already-issued ACM certificate. A hostname left off gets a certificate ocel requests, validates and deletes again."`
}
