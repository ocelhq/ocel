package gcp

type Options struct {
	Project string `json:"project,omitempty" doc:"The Google Cloud project to deploy into. Omit it and ocel reads GOOGLE_CLOUD_PROJECT, CLOUDSDK_CORE_PROJECT, then the credentials and gcloud config. ocel cost scan reads no credentials and runs no gcloud, so it needs the project here or in one of those two variables."`
	Region  string `json:"region" doc:"The region to deploy into. A project spans them all, so this names the one."`

	PreviewViewers []string `json:"previewViewers,omitempty" doc:"Who may open a preview deployed with no edge in front, which Identity-Aware Proxy serves: IAM members such as user:ana@example.com, group:team@example.com or domain:example.com."`
}
