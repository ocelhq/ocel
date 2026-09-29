package env

import (
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/variables"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

type envOptions struct {
	preview     bool
	folder      string
	environment string
	reveal      bool
	yes         bool
}

func (o envOptions) checkEnvironment() error {
	return commands.RefuseEnvironmentWithoutPreview(o.preview, o.environment)
}

type envRefOptions struct {
	project string
	folder  string
	key     string
}

func (o envRefOptions) target(slug, key string) *envvarsv1.Coordinate {
	project, name := o.project, o.key
	if project == "" {
		project = slug
	}
	if name == "" {
		name = key
	}
	return &envvarsv1.Coordinate{Slug: project, Folder: o.folder, Key: name}
}

func (o envOptions) tier() environmentv1.Tier {
	return commands.ChooseTier(o.preview)
}

func wireCoordinate(slug, key string, opts envOptions) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: slug, Folder: opts.folder, Key: key, Environment: opts.environment}
}

func envCoordinate(key string, opts envOptions) variables.Coordinate {
	return variables.Coordinate{Cell: variables.Cell{Key: key, Folder: opts.folder}, Environment: opts.environment}
}

func describeCell(key string, opts envOptions) string {
	out := key
	if opts.folder != "" {
		out += " in " + opts.folder
	}
	if opts.environment != "" {
		out += " for " + opts.environment
	}
	return out
}

func describeCoordinate(c *envvarsv1.Coordinate) string {
	out := c.GetSlug() + "/" + c.GetKey()
	if c.GetFolder() != "" {
		out += " in " + c.GetFolder()
	}
	if c.GetEnvironment() != "" {
		out += " for " + c.GetEnvironment()
	}
	return out
}
