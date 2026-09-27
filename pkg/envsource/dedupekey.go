package envsource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/envvars"
)

const dedupeKeySeparator = "\x00"

func DedupeKey(ctx context.Context, store envvars.Store, scope envvars.Scope, descriptor Descriptor) (string, error) {
	options := descriptor.Infisical
	if options == nil {
		return string(descriptor.Kind) + dedupeKeySeparator + scope.Project, nil
	}
	parts := []string{string(descriptor.Kind), options.Host, options.Project, options.Environment, options.Path, string(options.Auth.Method)}
	switch options.Auth.Method {
	case AuthIdentity:
		parts = append(parts, options.Auth.IdentityID)
	case AuthUniversal:
		for _, name := range options.Auth.Variables() {
			found, err := store.GetDereferenced(ctx, scope, envvars.Coordinate{Cell: envvars.Cell{Key: name}}, false)
			switch {
			case errors.Is(err, envvars.ErrNotFound), errors.Is(err, envvars.ErrDangling):
				parts = append(parts, "unset", scope.Project, name)
				continue
			case err != nil:
				return "", fmt.Errorf("read which value %s logs in to %s with: %w", name, descriptor.ID(), err)
			}
			parts = append(parts, found.Project, found.Coordinate.Folder, found.Coordinate.Key)
		}
	}
	return strings.Join(parts, dedupeKeySeparator), nil
}
