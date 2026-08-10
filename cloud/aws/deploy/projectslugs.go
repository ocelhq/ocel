package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func ProjectSlugsBesides(ctx context.Context, cfg ListConfig) ([]string, error) {
	ws, err := backendWorkspace(ctx, cfg.ProjectName, cfg.BackendURL, cfg.Passphrase, cfg.Region, cfg.Pulumi)
	if err != nil {
		return nil, err
	}
	summaries, err := ws.ListStacks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}
	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	return projectSlugsBesides(cfg.Slug, names), nil
}

func projectSlugsBesides(slug string, stackNames []string) []string {
	mine := safeName(slug)
	others := map[string]struct{}{}
	for _, name := range stackNames {
		scope, _, ok := strings.Cut(name, "--")
		if !ok || scope == "" {
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
	sort.Strings(out)
	return out
}
