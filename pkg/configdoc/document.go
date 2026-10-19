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
	Slug          string               `json:"slug" doc:"The project's deployment identity. Changing it forks a new project."`
	Bindings      Bindings             `json:"bindings,omitempty" doc:"Resources ocel binds instead of provisioning, keyed by resource type, then by the name the app declares."`
	Transforms    StringList           `json:"transforms,omitempty" doc:"Paths to transform modules applied while provisioning, in order. Each default-exports a defineTransform(...) result."`
	Discovery     *DiscoveryConfig     `json:"discovery,omitempty" doc:"Where the resources an app declares are found."`
	Provider      *ProviderDescriptor  `json:"provider,omitempty" doc:"The provider ocel deploys into, named alone or keyed by its identifier with its options as the value."`
	AllowDegraded []string             `json:"allowDegraded,omitempty" doc:"The needs this project waives rather than have a deploy refused over." enum:"edge-middleware,edge-runtime,ppr-resume,edge-cache,streaming,revalidation"`
	Apps          []AppConfig          `json:"apps,omitempty" doc:"The apps this project deploys. Left off, ocel deploys the app it detects at the project root."`
	Domains       *ProjectDomainConfig `json:"domains,omitempty" doc:"The hostnames this project is served on."`
	Registry      *RegistryConfig      `json:"registry,omitempty" doc:"Where this project's container images are pushed."`
	Protection    *ProtectionConfig    `json:"protection,omitempty" doc:"Which tiers ask for a password before they answer."`
	Lifecycle     *LifecycleConfig     `json:"lifecycle,omitempty" doc:"Commands ocel runs on this machine at points of a deploy."`
	EnvSource     *EnvSourceConfig     `json:"envSource,omitempty" doc:"Where each tier's values are read from."`
}

type ProtectionConfig struct {
	Production bool `json:"production,omitempty" doc:"Whether production asks for a password before it answers. Left off, false."`
	Preview    bool `json:"preview,omitempty" doc:"Whether every preview asks for a password before it answers. Left off, false."`
}

type LifecycleConfig struct {
	PreBuild *LifecycleCommand `json:"preBuild,omitempty" doc:"Runs once per deploy, after provisioning and before any app is built. A non-zero exit stops the deploy."`
}

type LifecycleCommand struct {
	Command  string `json:"command" doc:"The shell command to run, from the project directory, or from the app's directory when app is set."`
	App      string `json:"app,omitempty" doc:"The app whose variables the command also receives and whose directory it runs from."`
	Previews string `json:"previews,omitempty" doc:"Which previews run the command. Production always runs it. Left off, all." enum:"persistent,all,none"`
	Timeout  string `json:"timeout,omitempty" doc:"How long the command may run, as a duration such as 90s or 15m. Left off, 30m."`
}

func (LifecycleCommand) AlsoAString() {}

func (LifecycleCommand) Doc() string {
	return "A shell command ocel runs on this machine with the deployed environment's bindings."
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
	Paths []string `json:"paths,omitempty" doc:"Directories of infrastructure declarations, relative to the config. Left off, infra next to the config."`
}

type AppConfig struct {
	Name               string             `json:"name" doc:"The app's name. It must be a valid DNS label."`
	Path               string             `json:"path" doc:"The app's directory, relative to the config."`
	Framework          string             `json:"framework,omitempty" doc:"What the app is built with, when ocel is not to detect it from the app's own files." enum:"node,next,sveltekit,go,python,rust"`
	Entrypoint         string             `json:"entrypoint,omitempty" doc:"Where the app starts, if not the detected one: a node file, a go directory, a python module or a rust binary."`
	Compute            *ComputeDescriptor `json:"compute,omitempty" doc:"What the app runs on, named alone or keyed with its options. Left off, the provider's first compute."`
	Arch               string             `json:"arch,omitempty" doc:"The processor architecture the app is built for. Left off, what the provider runs the app on." enum:"x86_64,arm64"`
	Folder             string             `json:"folder,omitempty" doc:"The variables folder this app reads, when it does not read the project's own."`
	Domains            *AppDomainConfig   `json:"domains,omitempty" doc:"The hostnames this app is served on."`
	BuildWithResources *bool              `json:"buildWithResources,omitempty" doc:"Whether a Next app's build can reach its deployed resources, so prerendered pages can read them. Left off, true."`
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
	return "What the app runs on: serverless functions or one container image. Named alone, or keyed with its options."
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
		if slices.Contains(fieldNames(jsonFields(reflect.TypeFor[AppConfig]())), key) {
			return fmt.Errorf("%w; %s is an option of the app — write it beside compute", err, key)
		}
	}
	return err
}

type ServerlessCompute struct{}

type ContainerCompute struct {
	Image     *ImageConfig     `json:"image,omitempty" doc:"How the app's image is built."`
	Health    *HealthConfig    `json:"health,omitempty" doc:"How the app is checked before it is served."`
	Instances *InstancesConfig `json:"instances,omitempty" doc:"How many instances of the app run."`
}

type ImageConfig struct {
	Dockerfile string `json:"dockerfile,omitempty" doc:"The Dockerfile to build from. A relative path resolves against the app's directory."`
	Context    string `json:"context,omitempty" doc:"The directory the image is built from, relative to the project. Left off, the workspace root the app belongs to."`
	Command    string `json:"command,omitempty" doc:"The command that builds the app inside the image. Left off, the app's own build script runs."`
}

type InstancesConfig struct {
	Min *int `json:"min,omitempty" minimum:"0" doc:"The fewest instances kept running. Left off, 1, or 0 for an app a worker joins by name."`
	Max *int `json:"max,omitempty" minimum:"1" doc:"The most instances run at once. Left off, as many as min, or 1."`
}

type HealthConfig struct {
	Path string `json:"path,omitempty" doc:"The path the health check requests. Any 2xx means up. Left off, / or, on a VPS, the first common path that answers."`
}

type AppDomainConfig struct {
	Production StringList `json:"production,omitempty" doc:"The hostnames production is served on."`
}

type ProjectDomainConfig struct {
	Production StringList `json:"production,omitempty" doc:"The hostnames production is served on."`
	Preview    string     `json:"preview,omitempty" doc:"The wildcard hostname previews are served under, such as *.preview.example.com."`
}

type RegistryConfig struct {
	Server   string `json:"server" doc:"The registry host and namespace, such as ghcr.io/acme, with no scheme or credentials."`
	Username string `json:"username,omitempty" doc:"The username the push authenticates as, where the registry wants one."`
	Password string `json:"password" secret:"REGISTRY_TOKEN" doc:"The environment variable holding the password or token, written as \"${REGISTRY_TOKEN}\". Never the secret itself."`
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
