package gcp

type Options struct {
	Project string `json:"project" doc:"The Google Cloud project to deploy into."`
	Region  string `json:"region" doc:"The region to deploy into. A project spans them all, so this names the one."`
}
