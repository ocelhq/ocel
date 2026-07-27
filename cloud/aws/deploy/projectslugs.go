package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ProjectSlugsBesides names the other projects the account-global backend
// already holds stacks for, or nil when cfg.Slug itself already owns stacks
// there. It backs the CLI's slug-drift guard: since the slug is a project's
// only thread back to its infrastructure, a non-empty answer means this deploy
// would stand a brand-new project up beside existing ones — what a mistyped or
// renamed slug looks like. An empty backend answers nil, so a genuine first
// deploy is not flagged.
//
// It opens a bare Pulumi workspace over the same self-managed backend Destroy
// and ListPreviewStacks select against; the pure derivation it relies on is
// projectSlugsBesides.
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

// projectSlugsBesides recovers the distinct project scopes the backend's stack
// names carry and drops slug's own, returning nil when slug owns any of them.
//
// Every stack name a project produces — "<scope>--infra",
// "<scope>--<app>--<build>", "<scope>--preview-<pointer>--…" — is joined from
// segments that each ran through safeName, which collapses runs of "-" to one,
// so "--" appears in a name only as the delimiter after the project scope.
// The scope is therefore everything before the first "--". A name without one
// belongs to no project and is skipped. Results are sorted. Pure.
//
// The recovered scope is safeName(slug), not the slug itself: safeName
// normalizes and caps at maxSafeNamePrefixLen, so two slugs differing only in
// case, in repeated hyphens, or past that cap share one scope and are reported
// as one. That is the same identity the stacks themselves are keyed on, so the
// membership answer is exact; only the spelling shown back to the user is the
// normalized one.
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
