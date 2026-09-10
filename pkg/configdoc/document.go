package configdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Document struct {
	Schema        string               `json:"$schema,omitempty" doc:"The JSON Schema this config is written against. ocel init writes the schema shipped with the CLI that created it."`
	Slug          string               `json:"slug" doc:"The project's deployment identity. Every stack and resource ocel creates in your own account is keyed on it, so changing it forks a new project."`
	Bindings      Bindings             `json:"bindings,omitempty" doc:"Resources this project declares that ocel binds to a record your own infrastructure published, instead of provisioning them itself. Keyed by resource type, then by the name the app declares; the value is the name the record is published under. A name nothing has published refuses the deploy."`
	Discovery     *DiscoveryConfig     `json:"discovery,omitempty" doc:"Where the resources an app declares are found."`
	Provider      *ProviderDescriptor  `json:"provider,omitempty" doc:"The provider ocel deploy provisions into."`
	Edge          *EdgeDescriptor      `json:"edge,omitempty" doc:"The edge in front of the origin. Omit it and the provider fronts the deployment with its own default edge."`
	DNS           *DnsDescriptor       `json:"dns,omitempty" doc:"Where the project's hostname records are written."`
	AllowDegraded []string             `json:"allowDegraded,omitempty" doc:"The needs this project waives rather than have a deploy refused over." enum:"edge-middleware,edge-runtime,ppr-resume,edge-cache,streaming"`
	Apps          []AppConfig          `json:"apps,omitempty" doc:"The apps this project deploys. Left off, ocel detects one at the project root."`
	Domains       *ProjectDomainConfig `json:"domains,omitempty" doc:"The hostnames this project is served on."`
	Registry      *RegistryConfig      `json:"registry,omitempty" doc:"Where this project's container images are pushed."`
}

type DiscoveryConfig struct {
	Paths []string `json:"paths,omitempty" doc:"The directories holding infrastructure declarations, relative to the config. Left off, ocel reads the default discovery directory."`
}

type ProviderDescriptor struct {
	Name    string          `json:"name" doc:"The provider's name — the one ocel fetches, runs and deploys through."`
	Options json.RawMessage `json:"options,omitempty" doc:"The options this provider takes. The provider itself is what checks them."`
}

type EdgeDescriptor struct {
	Kind string `json:"kind" doc:"The edge the project's hostnames are served from."`
}

type DnsDescriptor struct {
	Kind string `json:"kind" doc:"The DNS the project's records are written into."`
	Zone string `json:"zone,omitempty" doc:"The zone the records are written into. Omit it and ocel picks the zone that covers the hostname."`
}

type AppConfig struct {
	Name       string           `json:"name" doc:"The app's name. It is a label of every resource the app deploys and of its preview hostname, so it is a DNS label."`
	Path       string           `json:"path" doc:"The app's directory, relative to the config."`
	Entrypoint string           `json:"entrypoint,omitempty" doc:"The file the app is served from, when it is not the one ocel would detect."`
	Compute    string           `json:"compute,omitempty" doc:"What the app runs on: serverless functions packed per route, or one container image serving everything." enum:"serverless,container"`
	Framework  string           `json:"framework,omitempty" doc:"What a serverless app is built with, when ocel is not to read it off the app's own manifest." enum:"node,next,go,python"`
	Arch       string           `json:"arch,omitempty" doc:"The processor architecture a serverless app's functions are built for. Left off, the provider's default architecture." enum:"x86_64,arm64"`
	Folder     string           `json:"folder,omitempty" doc:"The variables folder this app reads, when it does not read the project's own."`
	Domains    *AppDomainConfig `json:"domains,omitempty" doc:"The hostnames this app is served on."`
	Build      *BuildConfig     `json:"build,omitempty" doc:"How a container app's image is built."`
	Health     *HealthConfig    `json:"health,omitempty" doc:"How a container app is checked before it is served."`
}

type BuildConfig struct {
	Dockerfile string `json:"dockerfile,omitempty" doc:"The Dockerfile to build from. A relative path resolves against the app's directory."`
	Context    string `json:"context,omitempty" doc:"The directory the image is built from, relative to the project. Left off, it is the workspace root the app belongs to."`
	Command    string `json:"command,omitempty" doc:"The command that builds the app inside the image. Left off, the app's own build script runs."`
}

type HealthConfig struct {
	Path string `json:"path,omitempty" doc:"The path the check requests, off the app's own root. Any 2xx answer means up. Left off, the check requests /."`
}

type AppDomainConfig struct {
	Production StringList `json:"production,omitempty" doc:"The hostnames production is served on."`
}

type ProjectDomainConfig struct {
	Production StringList `json:"production,omitempty" doc:"The hostnames production is served on."`
	Preview    string     `json:"preview,omitempty" doc:"The wildcard hostname previews are served under, such as *.preview.example.com."`
}

type RegistryConfig struct {
	Server   string `json:"server" doc:"The registry host and the namespace images sit under, such as ghcr.io/acme. No scheme, and no credentials."`
	Username string `json:"username,omitempty" doc:"The username the push authenticates as, where the registry wants one."`
	Password string `json:"password" doc:"The name of the environment variable holding the password or token — not the secret itself."`
}

type StringList []string

func (s *StringList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		var list []string
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return err
		}
		*s = list
		return nil
	}
	var one string
	if err := json.Unmarshal(trimmed, &one); err != nil {
		return err
	}
	if one == "" {
		*s = nil
		return nil
	}
	*s = StringList{one}
	return nil
}

func (s StringList) checkShape(path string, value any) error {
	switch shaped := value.(type) {
	case string:
		return nil
	case []any:
		for i, item := range shaped {
			if _, ok := item.(string); !ok {
				return typeError(fmt.Sprintf("%s[%d]", path, i), "a hostname")
			}
		}
		return nil
	default:
		return typeError(path, "a hostname or a list of hostnames")
	}
}
