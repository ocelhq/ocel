package alb

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

type servedRelease struct {
	Release string `json:"release"`
	Tagged  bool   `json:"tagged,omitempty"`
}

func releaseTag(record router.DeploymentRecord) string {
	segments := strings.Split(record.IsrPrefix, "/")
	if len(segments) != 5 || segments[4] != "isr" {
		return ""
	}
	if _, err := naming.ParseRelease(segments[3]); err != nil {
		return ""
	}
	return segments[3]
}

func releaseTagged(record router.DeploymentRecord) bool {
	return record.Framework == buildoutput.FrameworkNext
}

func servedKey(pointer, app string) string { return pointer + "/" + app }

func (s *stack) purgeReplaced(ctx context.Context, move router.PointerMove, progress progress.Log) {
	if _, _, deployment := router.ParseDeploymentPointer(move.Pointer); deployment {
		return
	}
	pointer := router.ResolvePointer(move.Pointer)
	var tags []string
	for _, app := range slices.Sorted(maps.Keys(move.Records)) {
		record := move.Records[app]
		release := releaseTag(record)
		now := servedRelease{Release: release, Tagged: releaseTagged(record) && release != ""}
		key := servedKey(pointer, app)
		prior, known := s.recorded.Served[key]
		if known && prior.Tagged && prior.Release != now.Release {
			tags = append(tags, prior.Release)
		}
		switch {
		case release == "":
			delete(s.recorded.Served, key)
		case s.recorded.Served == nil:
			s.recorded.Served = map[string]servedRelease{key: now}
		default:
			s.recorded.Served[key] = now
		}
	}
	s.keep()
	urlMap := s.recorded.LoadBalancer.URLMap
	if len(tags) == 0 || urlMap == "" || progress == nil {
		return
	}
	cleared := strings.Join(tags, ", ")
	if err := s.e.deps.Routes.InvalidateTags(ctx, urlMap, tags); err != nil {
		progress.Warn(fmt.Sprintf("Could not clear release %s from Cloud CDN, so it serves the replaced pages until they expire: %v. "+
			"Clear it with: gcloud beta compute url-maps invalidate-cdn-cache %s --tags=%s --async",
			cleared, err, urlMap, strings.Join(tags, ",")))
		return
	}
	progress.Say("Cleared release " + cleared + " from Cloud CDN")
}

func (s *stack) forgetPointer(pointer string) {
	prefix := router.ResolvePointer(pointer) + "/"
	for key := range s.recorded.Served {
		if strings.HasPrefix(key, prefix) {
			delete(s.recorded.Served, key)
		}
	}
	if len(s.recorded.Served) == 0 {
		s.recorded.Served = nil
	}
	s.keep()
}
