package alb

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

type servedRelease struct {
	Release string `json:"release"`
	Tagged  bool   `json:"tagged,omitempty"`
}

func readReleaseTag(record router.ReleaseRecord) (string, error) {
	storage, err := record.ParseStorageCoordinate()
	if err != nil {
		return "", err
	}
	return storage.Release.String(), nil
}

func readReleaseTags(records map[string]router.ReleaseRecord) (map[string]string, error) {
	tags := make(map[string]string, len(records))
	for app, record := range records {
		tag, err := readReleaseTag(record)
		if err != nil {
			return nil, err
		}
		tags[app] = tag
	}
	return tags, nil
}

func isReleaseTagged(record router.ReleaseRecord) bool {
	return record.Framework == buildoutput.FrameworkNext && record.Physical == ""
}

func newServedKey(pointer, app string) string { return pointer + "/" + app }

func (s *stack) servedHostnames(move router.PointerMove, app string, record router.ReleaseRecord) []string {
	var hostnames []string
	for hostname, host := range s.recorded.Hosts {
		if _, _, deployment := router.ParseDeploymentPointer(host.Pointer); deployment {
			continue
		}
		if _, pinned := record.Revisions[host.Service]; host.App == app && host.Service != "" && pinned {
			hostnames = append(hostnames, hostname)
		}
	}
	for _, alias := range move.Hosts {
		if alias.App == app {
			hostnames = append(hostnames, alias.Hostname)
		}
	}
	slices.Sort(hostnames)
	return slices.Compact(hostnames)
}

func (s *stack) purgeReplaced(ctx context.Context, move router.PointerMove, releases map[string]string, log progress.Log) {
	if _, _, deployment := router.ParseDeploymentPointer(move.Pointer); deployment {
		return
	}
	pointer := router.ResolvePointer(move.Pointer)
	var tags, hostnames []string
	for _, app := range slices.Sorted(maps.Keys(move.Records)) {
		record := move.Records[app]
		release := releases[app]
		now := servedRelease{Release: release, Tagged: isReleaseTagged(record)}
		key := newServedKey(pointer, app)
		prior, known := s.recorded.Served[key]
		if known && prior.Tagged && prior.Release != now.Release {
			tags = append(tags, prior.Release)
		}
		sameRelease := known && prior.Release == now.Release
		if !sameRelease && (!known || !prior.Tagged || !now.Tagged) {
			hostnames = append(hostnames, s.servedHostnames(move, app, record)...)
		}
		if s.recorded.Served == nil {
			s.recorded.Served = map[string]servedRelease{}
		}
		s.recorded.Served[key] = now
	}
	s.keep()
	urlMap := s.recorded.LoadBalancer.URLMap
	if urlMap == "" {
		return
	}
	if log == nil {
		log = progress.Discard()
	}
	slices.Sort(tags)
	s.purgeTags(ctx, urlMap, slices.Compact(tags), log)
	slices.Sort(hostnames)
	s.purgeHostnames(ctx, urlMap, slices.Compact(hostnames), log)
}

func (s *stack) purgeTags(ctx context.Context, urlMap string, tags []string, progress progress.Log) {
	if len(tags) == 0 {
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

func (s *stack) purgeHostnames(ctx context.Context, urlMap string, hostnames []string, progress progress.Log) {
	if len(hostnames) == 0 {
		return
	}
	err := s.e.deps.Routes.InvalidateHostnames(ctx, urlMap, hostnames)
	if err == nil {
		progress.Say("Cleared " + strings.Join(hostnames, ", ") + " from Cloud CDN")
		return
	}
	commands := make([]string, len(hostnames))
	for i, hostname := range hostnames {
		commands[i] = fmt.Sprintf("gcloud compute url-maps invalidate-cdn-cache %s --host %s --path \"/*\" --async", urlMap, hostname)
	}
	progress.Warn(fmt.Sprintf("Could not clear %s from Cloud CDN, so they serve the replaced release's cached responses until they expire: %v. Clear them with: %s",
		strings.Join(hostnames, ", "), err, strings.Join(commands, "; ")))
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
