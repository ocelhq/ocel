package deploy

import (
	"context"
	"slices"
)

func ProjectSlugsBesides(ctx context.Context, index StackIndex, slug string) ([]string, error) {
	ix, err := stackIndex(index)
	if err != nil {
		return nil, err
	}
	scopes, err := ix.Projects(ctx)
	if err != nil {
		return nil, err
	}
	return projectSlugsBesides(slug, scopes), nil
}

func projectSlugsBesides(slug string, scopes []string) []string {
	mine := safeName(slug)
	others := map[string]struct{}{}
	for _, scope := range scopes {
		if scope == "" {
			continue
		}
		if scope == mine {
			return nil
		}
		others[scope] = struct{}{}
	}

	out := make([]string, 0, len(others))
	for scope := range others {
		out = append(out, scope)
	}
	slices.Sort(out)
	return out
}
