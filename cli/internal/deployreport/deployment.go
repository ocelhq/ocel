package deployreport

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/version"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const maxErrorRunes = 4000

type Attempt struct {
	Kind        consolev1.DeploymentKind
	Project     *project.Project
	Environment *environmentv1.Environment
	Target      string
	TraceID     string
	StartedAt   time.Time
	Apps        []*consolev1.App
	PromotionID string
	Tag         string
	Trigger     *consolev1.Trigger
	CI          *consolev1.CI
	Source      *consolev1.Source
}

func (a Attempt) Succeeded(finishedAt time.Time) *consolev1.Deployment {
	deployment := a.deployment(finishedAt)
	deployment.Outcome = consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED
	deployment.Promotion = &consolev1.Promotion{Id: a.PromotionID, Seq: deployment.GetFinishedAt().AsTime().UnixMilli(), Tag: a.Tag}
	return deployment
}

func (a Attempt) Failed(finishedAt time.Time, cause error) *consolev1.Deployment {
	deployment := a.deployment(finishedAt)
	deployment.Outcome = consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_FAILED
	deployment.Error = lastRunes(cause.Error(), maxErrorRunes)
	return deployment
}

func (a Attempt) deployment(finishedAt time.Time) *consolev1.Deployment {
	if finishedAt.Before(a.StartedAt) {
		finishedAt = a.StartedAt
	}
	deployment := &consolev1.Deployment{
		Id:          a.TraceID,
		Kind:        a.Kind,
		Environment: a.Environment,
		Provider:    &consolev1.Provider{Name: providerName(a.Project)},
		Target:      a.Target,
		StartedAt:   timestamppb.New(a.StartedAt),
		FinishedAt:  timestamppb.New(finishedAt),
		CliVersion:  version.Version,
		Apps:        a.Apps,
		Trigger:     a.Trigger,
		Ci:          a.CI,
		Source:      a.Source,
	}
	if kind := a.Project.EdgeKind(); kind != "" {
		deployment.Edge = &consolev1.Edge{Kind: string(kind)}
	}
	return deployment
}

func providerName(cfg *project.Project) string {
	if cfg.Provider == nil {
		return ""
	}
	return cfg.Provider.ID
}

func lastRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[len(runes)-limit:])
}

func NewEnvironmentEvent(kind consolev1.EnvironmentEventKind, env *environmentv1.Environment, traceID string, at time.Time, ci *consolev1.CI, source *consolev1.Source) *consolev1.EnvironmentEvent {
	return &consolev1.EnvironmentEvent{
		Id:          traceID,
		Kind:        kind,
		Environment: env,
		At:          timestamppb.New(at),
		Ci:          ci,
		Source:      source,
	}
}
