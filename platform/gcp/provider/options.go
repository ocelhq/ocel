package gcp

type Options struct {
	Project string `json:"project" doc:"The Google Cloud project to deploy into."`
	Region  string `json:"region" doc:"The region to deploy into. A project spans them all, so this names the one."`

	PreviewViewers []string `json:"previewViewers,omitempty" doc:"Who may open a preview deployed with no edge in front, which Identity-Aware Proxy serves: IAM members such as user:ana@example.com, group:team@example.com or domain:example.com."`
}
