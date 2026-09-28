package stackrecords

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	bootstrapKey = "settings"
	wildcardKey  = "preview"
)

func SchemaKey(tier environment.Tier) keyvalue.Key {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootSchema}.Key(string(tier))
}

func ProjectsPartition(tier environment.Tier) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootProjects}
}

func ProjectKey(tier environment.Tier, slug string) keyvalue.Key {
	return ProjectsPartition(tier).Key(slug)
}

func BootstrapKey(tier environment.Tier) keyvalue.Key {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootBootstrap}.Key(bootstrapKey)
}

func StacksPartition(tier environment.Tier, slug string) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootStacks, Path: []string{slug}}
}

func StackKey(tier environment.Tier, slug string, stack naming.StackName) keyvalue.Key {
	return StacksPartition(tier, slug).Key(stack.String())
}

func EnvironmentsPartition(tier environment.Tier, slug string) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootEnvironments, Path: []string{slug}}
}

func EnvironmentKey(tier environment.Tier, slug, env string) keyvalue.Key {
	return EnvironmentsPartition(tier, slug).Key(env)
}

func EdgeStacksPartition(tier environment.Tier) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootEdgeStacks}
}

func EdgeStackKey(tier environment.Tier, slug string) keyvalue.Key {
	return EdgeStacksPartition(tier).Key(slug)
}

func WildcardKey(tier environment.Tier) keyvalue.Key {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootWildcard}.Key(wildcardKey)
}
