package providerserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const httpLogSource = "http"

func (h *handlers) ReadLogs(ctx context.Context, req *contractv1.ReadLogsRequest, stream *connect.ServerStream[contractv1.ReadLogsResponse]) error {
	p, err := h.session.use()
	if err != nil {
		return err
	}
	tier, err := decodeTier(req.GetEnvironment().GetTier())
	if err != nil {
		return err
	}
	env, err := envName(req.GetEnvironment())
	if err != nil {
		return provider.RefusalError(err)
	}
	if req.GetSince() == nil {
		return provider.RefusalError(refusal.Refuse(refusal.CodeInvalid, "a log read names no start time, and without one it would scan the project's whole retention"))
	}
	targets, err := h.chooseLogTargets(ctx, p, tier, env, req)
	if err != nil {
		return provider.RefusalError(err)
	}
	if len(targets) > 0 {
		query := provider.LogQuery{
			Tier:     tier,
			Slug:     req.GetSlug(),
			Env:      env,
			Targets:  targets,
			Since:    req.GetSince().AsTime(),
			Limit:    int(req.GetLimit()),
			Contains: req.GetContains(),
		}
		if req.GetUntil() != nil {
			query.Until = req.GetUntil().AsTime()
		}
		err := p.Logs().Read(ctx, query, func(entries []provider.LogEntry) error {
			return stream.Send(&contractv1.ReadLogsResponse{Body: &contractv1.ReadLogsResponse_Batch{Batch: &contractv1.LogBatch{Entries: logEntriesProto(entries)}}})
		})
		var missing provider.LogTargetsMissing
		if errors.As(err, &missing) {
			for _, target := range missing.Targets {
				if err := sendLogNotice(stream, contractv1.LogNotice_KIND_SOURCE_GONE, fmt.Sprintf("%s (%s) of release %s no longer exists, so its logs are gone", target.App, target.Source, target.Release)); err != nil {
					return err
				}
			}
		} else if err != nil {
			return provider.RefusalError(err)
		}
	}
	return sendLogNotice(stream, contractv1.LogNotice_KIND_CAUGHT_UP, "")
}

func sendLogNotice(stream *connect.ServerStream[contractv1.ReadLogsResponse], kind contractv1.LogNotice_Kind, message string) error {
	return stream.Send(&contractv1.ReadLogsResponse{Body: &contractv1.ReadLogsResponse_Notice{Notice: &contractv1.LogNotice{Kind: kind, Message: message}}})
}

func (h *handlers) chooseLogTargets(ctx context.Context, p provider.Provider, tier environment.Tier, env string, req *contractv1.ReadLogsRequest) ([]provider.LogTarget, error) {
	recorded, err := stackrecords.List(ctx, p.KeyValues(), tier, req.GetSlug())
	if err != nil {
		return nil, err
	}
	var apps []stackrecords.NamedStack
	for _, stack := range recorded {
		if stack.Name.Env == env && stack.Kind == provider.StackApp {
			apps = append(apps, stack)
		}
	}
	if len(apps) == 0 {
		return nil, nil
	}
	if err := refuseUndeployedApps(apps, req.GetApps()); err != nil {
		return nil, err
	}
	apps = slices.DeleteFunc(apps, func(stack stackrecords.NamedStack) bool {
		return len(req.GetApps()) > 0 && !slices.Contains(req.GetApps(), stack.Name.App)
	})
	if !req.GetAllReleases() {
		live, err := h.readLiveBuilds(ctx, tier, env, req)
		if err != nil {
			return nil, err
		}
		apps = slices.DeleteFunc(apps, func(stack stackrecords.NamedStack) bool {
			build, promoted := live[stack.Name.App]
			return !promoted || build != stack.Build
		})
	}
	var targets []provider.LogTarget
	for _, stack := range apps {
		targets = append(targets, logTargetsOf(stack)...)
	}
	return targets, nil
}

func refuseUndeployedApps(deployed []stackrecords.NamedStack, named []string) error {
	known := map[string]bool{}
	for _, stack := range deployed {
		known[stack.Name.App] = true
	}
	for _, app := range named {
		if !known[app] {
			return refusal.Refuse(refusal.CodeInvalid, "%s is not deployed here; the deployed apps are %s",
				app, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
		}
	}
	return nil
}

func (h *handlers) readLiveBuilds(ctx context.Context, tier environment.Tier, env string, req *contractv1.ReadLogsRequest) (map[string]string, error) {
	session, err := h.openEdgeSession(ctx, tier, req.GetSlug(), req.GetEdge())
	if undeployed(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pointer, err := session.ledger.Read(ctx, pointerFor(tier, env))
	if err != nil {
		return nil, err
	}
	for _, promoted := range pointer.Promotions {
		if promoted.PromotionID == pointer.Active {
			return promoted.Builds, nil
		}
	}
	return nil, nil
}

func logTargetsOf(stack stackrecords.NamedStack) []provider.LogTarget {
	release := stack.Name.Release.String()
	var targets []provider.LogTarget
	for _, function := range stack.Functions {
		targets = append(targets, provider.LogTarget{App: stack.Name.App, Release: release, Source: logSourceOf(function.Name), Function: &function})
	}
	for _, container := range stack.Containers {
		targets = append(targets, provider.LogTarget{App: stack.Name.App, Release: release, Source: logSourceOf(container.Name), Container: &container})
	}
	return targets
}

func logSourceOf(name string) string {
	if worker, isWorker := resources.WorkerOf(name); isWorker {
		return worker
	}
	return httpLogSource
}

func logEntriesProto(entries []provider.LogEntry) []*contractv1.LogEntry {
	encoded := make([]*contractv1.LogEntry, 0, len(entries))
	for _, entry := range entries {
		encoded = append(encoded, &contractv1.LogEntry{
			Time:     timestamppb.New(entry.Time),
			App:      entry.App,
			Source:   entry.Source,
			Release:  entry.Release,
			Instance: entry.Instance,
			Stream:   logStreamProto(entry.Stream),
			Message:  entry.Message,
			Severity: entry.Severity,
			Failure:  entry.Failure,
		})
	}
	return encoded
}

func logStreamProto(stream provider.LogStream) contractv1.LogStream {
	switch stream {
	case provider.LogStreamStdout:
		return contractv1.LogStream_LOG_STREAM_STDOUT
	case provider.LogStreamStderr:
		return contractv1.LogStream_LOG_STREAM_STDERR
	}
	return contractv1.LogStream_LOG_STREAM_UNSPECIFIED
}
