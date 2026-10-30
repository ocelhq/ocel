package stackrecords

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/records"
)

const (
	wildcardKey      = "preview"
	previewKeyRecord = "previewkey"
	gatesRecord      = "gates"
	partsSegment     = "parts"
	wantsSegment     = "wants"
)

const (
	RootProjects          records.Root = "projects"
	RootStacks            records.Root = "stacks"
	RootEnvironments      records.Root = "environments"
	RootEnvironmentLeases records.Root = "environmentleases"
	RootProjectLeases     records.Root = "projectleases"
	RootBootstrap         records.Root = "bootstrap"
	RootEdgeStacks        records.Root = "edgestacks"
	RootWildcard          records.Root = "wildcard"
	RootRouters           records.Root = "routers"
)

func ProjectsPartition(tier environment.Tier) records.Partition {
	return records.Partition{Tier: tier, Root: RootProjects}
}

func ProjectKey(tier environment.Tier, slug string) records.Key {
	return ProjectsPartition(tier).Key(slug)
}

func PartsUsedKey(tier environment.Tier, project string) records.Key {
	return ProjectsPartition(tier).Key(project, partsSegment)
}

func bootstrapPartition(tier environment.Tier) records.Partition {
	return records.Partition{Tier: tier, Root: RootBootstrap}
}

func BootstrapPartKey(tier environment.Tier, name string) records.Key {
	return bootstrapPartition(tier).Key(partsSegment, name)
}

func BootstrapWantKey(tier environment.Tier, project, part string) records.Key {
	return bootstrapPartition(tier).Key(wantsSegment, project, part)
}

func NewPreviewKeyRecordKey() records.Key {
	return records.Partition{Tier: environment.TierPreview, Root: RootBootstrap}.Key(previewKeyRecord)
}

func NewGatesRecordKey(tier environment.Tier) records.Key {
	return records.Partition{Tier: tier, Root: RootBootstrap}.Key(gatesRecord)
}

func StacksPartition(tier environment.Tier, slug string) records.Partition {
	return records.Partition{Tier: tier, Root: RootStacks, Path: []string{slug}}
}

func StackKey(tier environment.Tier, slug string, stack naming.StackName) records.Key {
	return StacksPartition(tier, slug).Key(stack.String())
}

const appImagesSegment = "images"

func appImagesKey(tier environment.Tier, slug string, stack naming.StackName) records.Key {
	return StacksPartition(tier, slug).Key(appImagesSegment, stack.App, stack.String())
}

func EnvironmentsPartition(tier environment.Tier, slug string) records.Partition {
	return records.Partition{Tier: tier, Root: RootEnvironments, Path: []string{slug}}
}

func EnvironmentKey(tier environment.Tier, slug, env string) records.Key {
	return EnvironmentsPartition(tier, slug).Key(env)
}

func EdgeStacksPartition(tier environment.Tier) records.Partition {
	return records.Partition{Tier: tier, Root: RootEdgeStacks}
}

func EdgeStackKey(tier environment.Tier, slug string) records.Key {
	return EdgeStacksPartition(tier).Key(slug)
}

func WildcardKey(tier environment.Tier) records.Key {
	return records.Partition{Tier: tier, Root: RootWildcard}.Key(wildcardKey)
}

func OwnerOf(key records.Key) (records.Owner, bool) {
	partition, path := key.Partition, key.Path
	switch partition.Root {
	case RootProjects:
		if len(path) == 1 || (len(path) == 2 && path[1] == partsSegment) {
			return records.Owner{Project: path[0]}, true
		}
	case RootStacks:
		if len(partition.Path) == 1 {
			return ownerOfStackKey(partition.Path[0], path)
		}
	case RootEnvironments, RootEnvironmentLeases:
		if len(partition.Path) == 1 && len(path) == 1 {
			return records.Owner{Project: partition.Path[0], Environment: path[0]}, true
		}
	case RootProjectLeases, RootEdgeStacks:
		if len(path) == 1 {
			return records.Owner{Project: path[0]}, true
		}
	case RootWildcard:
		if len(path) == 1 && path[0] == wildcardKey {
			return records.Owner{Shared: true}, true
		}
	case RootBootstrap:
		return ownerOfBootstrapKey(path)
	}
	return records.Owner{}, false
}

func ownerOfStackKey(slug string, path []string) (records.Owner, bool) {
	var stack string
	switch {
	case len(path) == 1:
		stack = path[0]
	case len(path) == 3 && path[0] == appImagesSegment:
		stack = path[2]
	case len(path) == 2 && path[0] == resourcesSegment:
		stack = path[1]
	default:
		return records.Owner{}, false
	}
	name, err := naming.ParseStackName(stack)
	if err != nil {
		return records.Owner{}, false
	}
	return records.Owner{Project: slug, Environment: name.Env}, true
}

func ownerOfBootstrapKey(path []string) (records.Owner, bool) {
	switch {
	case len(path) == 2 && path[0] == partsSegment:
		return records.Owner{Shared: true}, true
	case len(path) == 3 && path[0] == wantsSegment:
		return records.Owner{Project: path[1]}, true
	case len(path) == 1 && (path[0] == previewKeyRecord || path[0] == gatesRecord):
		return records.Owner{Shared: true}, true
	}
	return records.Owner{}, false
}
