package envsource

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/envvars"
)

const dedupeKeySeparator = "\x00"

func DedupeKey(ctx context.Context, store envvars.Store, scope envvars.Scope, descriptor Descriptor) string {
	options := descriptor.Infisical
	if options == nil {
		return string(descriptor.Kind) + dedupeKeySeparator + scope.Project
	}
	parts := []string{string(descriptor.Kind), options.Host, options.Project, options.Environment, options.Path, string(options.Auth.Method)}
	switch options.Auth.Method {
	case AuthIdentity:
		parts = append(parts, options.Auth.IdentityID)
	case AuthUniversal:
		for _, name := range options.Auth.Variables() {
			found, err := store.GetDereferenced(ctx, scope, envvars.Coordinate{Cell: envvars.Cell{Key: name}}, false)
			if err != nil {
				parts = append(parts, "unset", scope.Project, name)
				continue
			}
			parts = append(parts, found.Project, found.Coordinate.Folder, found.Coordinate.Key)
		}
	}
	return strings.Join(parts, dedupeKeySeparator)
}
