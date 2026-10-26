package alb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
)

func (s *stack) applyAppPatches(ctx context.Context, progress progress.Log) error {
	if s.e.deps.ReadApps == nil {
		return nil
	}
	evaluated := s.e.deps.ReadApps(s.state.Tier, s.state.Slug)
	recorded := s.recorded.Patches
	next := maps.Clone(recorded)
	if next == nil {
		next = map[string]recordedPatches{}
	}
	var changed []string
	for _, app := range slices.Sorted(maps.Keys(evaluated)) {
		patches := patchesOf(evaluated[app])
		if patches.equal(recorded[app]) || (patches.isEmpty() && recorded[app].isEmpty()) {
			continue
		}
		if patches.isEmpty() {
			delete(next, app)
		} else {
			next[app] = patches
		}
		changed = append(changed, app)
	}
	if len(changed) == 0 {
		return nil
	}
	if len(next) == 0 {
		next = nil
	}
	s.recorded.Patches = next
	if !s.servesAnyOf(changed) {
		s.keep()
		return nil
	}
	progress.Say(fmt.Sprintf("Applying the transforms that patch the %s edge resources of %s: they differ from the ones they were provisioned with", Kind, strings.Join(changed, ", ")))
	if err := s.raise(ctx, s.recorded.Hosts); err != nil {
		s.recorded.Patches = recorded
		return errors.Join(err, s.raise(ctx, s.recorded.Hosts))
	}
	s.keep()
	return nil
}

func (s *stack) servesAnyOf(apps []string) bool {
	for _, host := range s.recorded.Hosts {
		if slices.Contains(apps, host.App) {
			return true
		}
	}
	return false
}
