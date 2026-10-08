package configdoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type Document struct {
	Schema        string               `json:"$schema,omitempty" doc:"The JSON Schema editors complete and validate this config against. ocel init writes it."`
	Slug          string               `json:"slug" doc:"The project's deployment identity. Every stack and resource ocel creates in your own account is keyed on it, so changing it forks a new project."`
	Bindings      Bindings             `json:"bindings,omitempty" doc:"Resources this project declares that ocel binds instead of provisioning them itself. Keyed by resource type, then by the name the app declares. The value is \"@\" followed by the name a record your own infrastructure published, such as \"@warehouse\", and a name nothing has published refuses the deploy; or the record written inline, checked at deploy rather than provisioned, optionally keyed by the tier it serves."`
	Transforms    StringList           `json:"transforms,omitempty" doc:"Transform modules applied while provisioning, in order — later modules win where their patches collide. Each is a path to a module whose default export is a defineTransform(...) result, keyed by the provider it patches."`
	Discovery     *DiscoveryConfig     `json:"discovery,omitempty" doc:"Where the resources an app declares are found."`
	Provider      *ProviderDescriptor  `json:"provider,omitempty" doc:"The provider ocel deploy provisions into, keyed by its identifier with its options as the value. Its options also name the edge in front of the origin and the DNS service hostname records are written into. A provider that needs no options may be named alone."`
	AllowDegraded []string             `json:"allowDegraded,omitempty" doc:"The needs this project waives rather than have a deploy refused over." enum:"edge-middleware,edge-runtime,ppr-resume,edge-cache,streaming"`
	Apps          []AppConfig          `json:"apps,omitempty" doc:"The apps this project deploys. Left off, ocel detects one at the project root."`
	Domains       *ProjectDomainConfig `json:"domains,omitempty" doc:"The hostnames this project is served on."`
	Registry      *RegistryConfig      `json:"registry,omitempty" doc:"Where this project's container images are pushed."`
	Lifecycle     *LifecycleConfig     `json:"lifecycle,omitempty" doc:"Commands ocel runs on this machine at points of a deploy."`
	EnvSource     *EnvSourceConfig     `json:"envSource,omitempty" doc:"Where each tier's values are read from. A tier left off reads its default: ocel's own store in your account for production and preview, the project's .env file for dev."`
}

type LifecycleConfig struct {
	PreBuild *LifecycleCommand `json:"preBuild,omitempty" doc:"Runs once per deploy, after the project's infrastructure is provisioned and before any app is built, with the bindings of the deployed environment. A non-zero exit or a timeout stops the deploy before it builds anything, and nothing is promoted. Written as the command alone, or as an object."`
}

type LifecycleCommand struct {
	Command  string `json:"command" doc:"The shell command to run, from the project directory, or from the app's directory when app is set."`
	App      string `json:"app,omitempty" doc:"The app whose variables the command also receives, and whose directory it runs from. Left off, the command gets the bindings alone and runs from the project directory."`
	Previews string `json:"previews,omitempty" doc:"Which previews run the command: persistent previews, which have infrastructure of their own, all previews, or none. Production always runs it. Left off, persistent: an ephemeral preview gets no infrastructure of its own and shares the preview tier's databases, so a migration there would change what every other ephemeral preview reads." enum:"persistent,all,none"`
	Timeout  string `json:"timeout,omitempty" doc:"How long the command may run, as a duration such as 90s or 15m, before it is stopped and the deploy fails. Left off, 30m."`
}

func (LifecycleCommand) AlsoAString() {}

func (LifecycleCommand) Doc() string {
	return "A shell command ocel runs on this machine with the bindings of the deployed environment, and where, when and for how long it runs."
}

func (c *LifecycleCommand) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var command string
		if err := json.Unmarshal(trimmed, &command); err != nil {
			return err
		}
		*c = LifecycleCommand{Command: command}
		return nil
	}
	type fields LifecycleCommand
	var decoded fields
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	*c = LifecycleCommand(decoded)
	return nil
}

type DiscoveryConfig struct {
	Paths []string `json:"paths,omitempty" doc:"The directories containing infrastructure declarations, relative to the config. Left off, ocel reads the default discovery directory."`
}

type AppConfig struct {
	Name               string             `json:"name" doc:"The app's name. It is a label of every resource the app deploys and of its preview hostname, so it is a DNS label."`
	Path               string             `json:"path" doc:"The app's directory, relative to the config."`
	Compute            *ComputeDescriptor `json:"compute,omitempty" doc:"What the app runs on, keyed by the compute with its options as the value, or named alone. Left off, the first compute the provider runs."`
	Arch               string             `json:"arch,omitempty" doc:"The processor architecture an app's functions or container image are built for. Left off, what the provider runs the app on." enum:"x86_64,arm64"`
	Folder             string             `json:"folder,omitempty" doc:"The variables folder this app reads, when it does not read the project's own."`
	Domains            *AppDomainConfig   `json:"domains,omitempty" doc:"The hostnames this app is served on."`
	BuildWithResources *bool              `json:"buildWithResources,omitempty" doc:"Whether ocel deploy lets the app's build reach the resources it uses in the deployed environment, so a page prerendered at build time reads them. Only a Next app's build reaches them, on serverless or container compute: its postgres databases and kv stores over ports the provider forwards, and its buckets through the binding proxy the provider serves. Any other build goes without. Left off, true. Set false to build as if no resource were provisioned."`
}

type ComputeDescriptor struct {
	Serverless *ServerlessCompute `json:"serverless,omitempty" doc:"Serverless functions packed per route, which scale themselves."`
	Container  *ContainerCompute  `json:"container,omitempty" doc:"One container image serving every route."`
}

const (
	computeServerless = "serverless"
	computeContainer  = "container"
)

func (ComputeDescriptor) Shorthands() []string { return []string{computeServerless, computeContainer} }

func (ComputeDescriptor) Doc() string {
	return "What the app runs on: serverless functions packed per route, or one container image serving everything. Named alone, or keyed by the compute with its options as the value."
}

func (c *ComputeDescriptor) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var named string
		if err := json.Unmarshal(trimmed, &named); err != nil {
			return err
		}
		switch named {
		case computeServerless:
			*c = ComputeDescriptor{Serverless: &ServerlessCompute{}}
		case computeContainer:
			*c = ComputeDescriptor{Container: &ContainerCompute{}}
		default:
			return fmt.Errorf("compute %q is none of %s", named, strings.Join(c.Shorthands(), ", "))
		}
		return nil
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keyed); err != nil {
		return err
	}
	if len(keyed) != 1 {
		return fmt.Errorf("compute must set exactly one of the keys %s", strings.Join(KeysOf(ComputeDescriptor{}), ", "))
	}
	type fields ComputeDescriptor
	var decoded fields
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	*c = ComputeDescriptor(decoded)
	if _, set := keyed[computeServerless]; set && c.Serverless == nil {
		c.Serverless = &ServerlessCompute{}
	}
	if _, set := keyed[computeContainer]; set && c.Container == nil {
		c.Container = &ContainerCompute{}
	}
	return nil
}

func (c ComputeDescriptor) checkShape(path string, value any) error {
	err := checkKeyed(path, reflect.TypeFor[ComputeDescriptor](), c.Shorthands(), value)
	var unknown UnknownKeyError
	if !errors.As(err, &unknown) {
		return err
	}
	options := map[string]reflect.Type{
		computeServerless: reflect.TypeFor[ServerlessCompute](),
		computeContainer:  reflect.TypeFor[ContainerCompute](),
	}
	for compute := range options {
		key := strings.TrimPrefix(unknown.Path, JoinPath(path, compute)+".")
		if key == unknown.Path || strings.ContainsAny(key, ".[") {
			continue
		}
		for other, accepts := range options {
			if other != compute && slices.Contains(fieldNames(jsonFields(accepts)), key) {
				return fmt.Errorf("%w; %s is an option of %s compute — write compute: { %s: { %s: … } }", err, key, other, other, key)
			}
		}
	}
	return err
}

type ServerlessCompute struct {
	Framework  string `json:"framework,omitempty" doc:"What the app is built with, when ocel is not to read it off the app's own manifest." enum:"node,next,sveltekit,go,python,rust"`
	Entrypoint string `json:"entrypoint,omitempty" doc:"The file a node app is served from, or the directory of a go app's main package, when it is not the one ocel would detect."`
}

type ContainerCompute struct {
	Image     *ImageConfig     `json:"image,omitempty" doc:"How the app's image is built."`
	Health    *HealthConfig    `json:"health,omitempty" doc:"How the app is checked before it is served."`
	Instances *InstancesConfig `json:"instances,omitempty" doc:"How many instances of the app run."`
}

type ImageConfig struct {
	Dockerfile string `json:"dockerfile,omitempty" doc:"The Dockerfile to build from. A relative path resolves against the app's directory."`
	Context    string `json:"context,omitempty" doc:"The directory the image is built from, relative to the project. Left off, it is the workspace root the app belongs to."`
	Command    string `json:"command,omitempty" doc:"The command that builds the app inside the image. Left off, the app's own build script runs."`
}

type InstancesConfig struct {
	Min *int `json:"min,omitempty" minimum:"0" doc:"The fewest instances kept running, however quiet the app is. Left off, 1."`
	Max *int `json:"max,omitempty" minimum:"1" doc:"The most instances run at once, however busy the app is. Left off, as many as min, or 1."`
}

type HealthConfig struct {
	Path string `json:"path,omitempty" doc:"The path the check requests, off the app's own root. Any 2xx answer means up. Left off, a VPS finds the path by probing /up, /health, /healthz and / in turn and keeps the first that exists, and AWS and Google Cloud request /."`
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
	Password string `json:"password" secret:"REGISTRY_TOKEN" doc:"The environment variable containing the password or token, written as \"${REGISTRY_TOKEN}\" and read where the push authenticates — never the secret itself."`
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
				return typeError(IndexPath(path, i), "a hostname")
			}
		}
		return nil
	default:
		return typeError(path, "a hostname or a list of hostnames")
	}
}
