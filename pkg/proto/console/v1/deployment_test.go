package consolev1_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

var started = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func validDeployment() *consolev1.Deployment {
	return &consolev1.Deployment{
		Id:      traceID,
		Kind:    consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY,
		Outcome: consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED,
		Slug:    "shop",
		Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PRODUCTION,
		},
		Provider:   &consolev1.Provider{Name: "aws", Region: "eu-west-1"},
		Target:     "aws/123456789012",
		StartedAt:  timestamppb.New(started),
		FinishedAt: timestamppb.New(started.Add(time.Minute)),
		CliVersion: "0.1.0",
		Promotion:  &consolev1.Promotion{Id: "p_01", Seq: 3},
		Trigger:    &consolev1.Trigger{Kind: consolev1.TriggerKind_TRIGGER_KIND_CLI},
		Apps: []*consolev1.App{{
			Name:      "web",
			BuildId:   "b_01",
			Release:   "r_01",
			Outcome:   consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED,
			Compute:   consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS,
			Framework: buildoutput.FrameworkNext,
			Urls:      []string{"https://shop.example.com"},
			Variables: []*resourcesv1.VariableDefinition{{
				Key:      "STRIPE_KEY",
				Class:    resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
				Required: true,
				Group:    "stripe",
			}},
		}},
		Resources: []*consolev1.Resource{{
			Name: "db",
			Type: string(provider.BindingPostgres),
		}},
		Links: []*consolev1.Link{{
			App:      "web",
			Resource: "db",
			Grants:   []*consolev1.Grant{{Label: "read", Actions: []string{"connect"}}},
		}},
		Usages: []*consolev1.Usage{{
			App:      "web",
			Resource: "db",
			Files:    []string{"src/db.ts"},
		}},
		VariableGroups: []*resourcesv1.GroupDefinition{{Key: "stripe", Required: true}},
		Source:         &consolev1.Source{Commit: "0123456789abcdef", Branch: "main"},
		Ci: &consolev1.CI{
			Name:   "github-actions",
			Repo:   "ocelhq/shop",
			RunUrl: "https://github.com/ocelhq/shop/actions/runs/1",
		},
		Spans: []*tracev1.Span{{Name: "deploy"}},
	}
}

func validEnvironmentEvent() *consolev1.EnvironmentEvent {
	return &consolev1.EnvironmentEvent{
		Kind:        consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_PREVIEW_REMOVED,
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-12"},
		At:          timestamppb.New(started),
	}
}

func failedDeployment() *consolev1.Deployment {
	d := validDeployment()
	d.Outcome = consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_FAILED
	d.Promotion = nil
	d.Error = "provisioning failed"
	return d
}

type refusal[T proto.Message] struct {
	name   string
	start  func() T
	mutate func(T)
	rule   string
}

func requireRefusals[T proto.Message](t *testing.T, valid func() T, cases []refusal[T]) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := tc.start
			if start == nil {
				start = valid
			}
			msg := start()
			tc.mutate(msg)
			err := protovalidate.Validate(msg)
			if err == nil {
				t.Fatal("expected protovalidate to refuse it")
			}
			if tc.rule == "" {
				return
			}
			var violations *protovalidate.ValidationError
			if !errors.As(err, &violations) {
				t.Fatalf("expected a validation error, got %v", err)
			}
			for _, v := range violations.Violations {
				if v.Proto.GetRuleId() == tc.rule {
					return
				}
			}
			t.Fatalf("expected rule %s among %v", tc.rule, err)
		})
	}
}

func requireAccepted(t *testing.T, name string, msg proto.Message) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if err := protovalidate.Validate(msg); err != nil {
			t.Fatalf("expected protovalidate to accept it, got %v", err)
		}
	})
}

func TestProtovalidateAcceptsEveryDeploymentTheCLICanReport(t *testing.T) {
	requireAccepted(t, "a succeeded deploy that names its promotion", validDeployment())
	requireAccepted(t, "a failed deploy with no promotion", failedDeployment())

	unclassified := validDeployment()
	unclassified.Apps[0].Variables[0].Class = resourcesv1.VariableClass_VARIABLE_CLASS_UNSPECIFIED
	requireAccepted(t, "a variable declared with no class", unclassified)

	uncommitted := validDeployment()
	uncommitted.Source = &consolev1.Source{Branch: "main"}
	requireAccepted(t, "a repository with no commits names only its branch", uncommitted)

	unframed := validDeployment()
	unframed.Apps[0].Framework = ""
	requireAccepted(t, "an app with no framework", unframed)

	for _, framework := range buildoutput.Frameworks() {
		d := validDeployment()
		d.Apps[0].Framework = framework
		requireAccepted(t, "an app built with "+framework, d)
	}

	for encoded := range bindingsv1.BindingType_name {
		kind, known := provider.BindingTypeFromProto(bindingsv1.BindingType(encoded))
		if !known {
			continue
		}
		d := validDeployment()
		d.Resources[0].Type = string(kind)
		requireAccepted(t, "a resource bound as "+string(kind), d)
	}
}

func TestProtovalidateRefusesADeploymentThatBreaksARule(t *testing.T) {
	requireRefusals(t, validDeployment, []refusal[*consolev1.Deployment]{
		{
			name: "a succeeded rollback names the promotion it made live",
			mutate: func(d *consolev1.Deployment) {
				d.Kind = consolev1.DeploymentKind_DEPLOYMENT_KIND_ROLLBACK
				d.Promotion = nil
			},
			rule: "deployment.promotion_on_success",
		},
		{
			name: "a succeeded preview up names the promotion it made live",
			mutate: func(d *consolev1.Deployment) {
				d.Kind = consolev1.DeploymentKind_DEPLOYMENT_KIND_PREVIEW_UP
				d.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-12"}
				d.Promotion = nil
			},
			rule: "deployment.promotion_on_success",
		},
		{
			name:   "a failed deployment made nothing live, so it names no promotion",
			start:  failedDeployment,
			mutate: func(d *consolev1.Deployment) { d.Promotion = &consolev1.Promotion{Id: "p_01", Seq: 3} },
			rule:   "deployment.no_promotion_on_failure",
		},
		{name: "the promotion id is required", mutate: func(d *consolev1.Deployment) { d.Promotion.Id = "" }},
		{name: "the promotion seq comes from the ledger and is positive", mutate: func(d *consolev1.Deployment) { d.Promotion.Seq = 0 }},
		{name: "the kind is required", mutate: func(d *consolev1.Deployment) { d.Kind = consolev1.DeploymentKind_DEPLOYMENT_KIND_UNSPECIFIED }},
		{name: "the kind is a defined one", mutate: func(d *consolev1.Deployment) { d.Kind = 99 }},
		{name: "the outcome is required", mutate: func(d *consolev1.Deployment) { d.Outcome = consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_UNSPECIFIED }},
		{name: "the id is a 32 digit lowercase hex trace id", mutate: func(d *consolev1.Deployment) { d.Id = "4BF92F3577B34DA6A3CE929D0E0E4736" }},
		{name: "an all zero trace id is invalid", mutate: func(d *consolev1.Deployment) { d.Id = strings.Repeat("0", 32) }},
		{name: "the slug is required", mutate: func(d *consolev1.Deployment) { d.Slug = "" }},
		{name: "the environment is required", mutate: func(d *consolev1.Deployment) { d.Environment = nil }},
		{
			name:   "the environment names its tier",
			mutate: func(d *consolev1.Deployment) { d.Environment.Tier = environmentv1.Tier_TIER_UNSPECIFIED },
			rule:   "deployment.environment_tier",
		},
		{
			name:   "a preview up deploys to the preview tier",
			mutate: func(d *consolev1.Deployment) { d.Kind = consolev1.DeploymentKind_DEPLOYMENT_KIND_PREVIEW_UP },
			rule:   "deployment.preview_up_in_preview",
		},
		{name: "the provider is required", mutate: func(d *consolev1.Deployment) { d.Provider = nil }},
		{name: "the provider name is required", mutate: func(d *consolev1.Deployment) { d.Provider.Name = "" }},
		{name: "the target names a vendor and the account it fingerprints", mutate: func(d *consolev1.Deployment) { d.Target = "aws" }},
		{name: "a target has no whitespace", mutate: func(d *consolev1.Deployment) { d.Target = "aws/12 34" }},
		{name: "the start time is required", mutate: func(d *consolev1.Deployment) { d.StartedAt = nil }},
		{name: "the finish time is required", mutate: func(d *consolev1.Deployment) { d.FinishedAt = nil }},
		{
			name:   "a deployment finishes at or after it starts",
			mutate: func(d *consolev1.Deployment) { d.FinishedAt = timestamppb.New(started.Add(-time.Second)) },
			rule:   "deployment.finished_after_started",
		},
		{name: "an app is named", mutate: func(d *consolev1.Deployment) { d.Apps[0].Name = "" }},
		{name: "an app outcome is required", mutate: func(d *consolev1.Deployment) { d.Apps[0].Outcome = consolev1.AppOutcome_APP_OUTCOME_UNSPECIFIED }},
		{name: "an app compute is required", mutate: func(d *consolev1.Deployment) { d.Apps[0].Compute = consolev1.ComputeKind_COMPUTE_KIND_UNSPECIFIED }},
		{name: "an app framework is one the CLI builds", mutate: func(d *consolev1.Deployment) { d.Apps[0].Framework = "nextjs" }},
		{name: "an app url is a url", mutate: func(d *consolev1.Deployment) { d.Apps[0].Urls = []string{"not a url"} }},
		{
			name:   "a succeeded app names its build",
			mutate: func(d *consolev1.Deployment) { d.Apps[0].BuildId = "" },
			rule:   "app.build_on_success",
		},
		{
			name:   "a succeeded app names its release",
			mutate: func(d *consolev1.Deployment) { d.Apps[0].Release = "" },
			rule:   "app.release_on_success",
		},
		{
			name:   "app names are unique",
			mutate: func(d *consolev1.Deployment) { d.Apps = append(d.Apps, proto.Clone(d.Apps[0]).(*consolev1.App)) },
			rule:   "deployment.unique_app_names",
		},
		{name: "a variable is named", mutate: func(d *consolev1.Deployment) { d.Apps[0].Variables[0].Key = "" }},
		{name: "a variable key has no control character", mutate: func(d *consolev1.Deployment) { d.Apps[0].Variables[0].Key = "STRIPE\nKEY" }},
		{
			name:   "a variable description is one line of at most 120 bytes",
			mutate: func(d *consolev1.Deployment) { d.Apps[0].Variables[0].Description = strings.Repeat("x", 121) },
			rule:   "variables.definition.description",
		},
		{name: "a variable folder is an absolute path", mutate: func(d *consolev1.Deployment) { d.Apps[0].Variables[0].Folders = []string{"apps/web"} }},
		{
			name:   "a variable names a group the project defines",
			mutate: func(d *consolev1.Deployment) { d.Apps[0].Variables[0].Group = "unknown" },
			rule:   "deployment.variable_groups_defined",
		},
		{
			name:   "variable groups are unique",
			mutate: func(d *consolev1.Deployment) { d.VariableGroups = append(d.VariableGroups, d.VariableGroups[0]) },
			rule:   "deployment.unique_variable_groups",
		},
		{name: "a resource is named", mutate: func(d *consolev1.Deployment) { d.Resources[0].Name = "" }},
		{name: "a resource type is one the CLI binds", mutate: func(d *consolev1.Deployment) { d.Resources[0].Type = "container" }},
		{
			name: "resource names are unique",
			mutate: func(d *consolev1.Deployment) {
				d.Resources = append(d.Resources, proto.Clone(d.Resources[0]).(*consolev1.Resource))
			},
			rule: "deployment.unique_resource_names",
		},
		{
			name:   "a link names a known app",
			mutate: func(d *consolev1.Deployment) { d.Links[0].App = "ghost" },
			rule:   "deployment.links_reference_known",
		},
		{
			name:   "a link names a known resource",
			mutate: func(d *consolev1.Deployment) { d.Links[0].Resource = "ghost" },
			rule:   "deployment.links_reference_known",
		},
		{
			name:   "a usage names a known app",
			mutate: func(d *consolev1.Deployment) { d.Usages[0].App = "ghost" },
			rule:   "deployment.usages_reference_known",
		},
		{
			name:   "a usage names a known resource",
			mutate: func(d *consolev1.Deployment) { d.Usages[0].Resource = "ghost" },
			rule:   "deployment.usages_reference_known",
		},
		{name: "a commit is at least seven characters", mutate: func(d *consolev1.Deployment) { d.Source.Commit = "abc12" }},
		{name: "a commit is lowercase hex", mutate: func(d *consolev1.Deployment) { d.Source.Commit = "not-a-commit" }},
		{name: "a ci run names its ci", mutate: func(d *consolev1.Deployment) { d.Ci.Name = "" }},
		{name: "a ci run url is a url", mutate: func(d *consolev1.Deployment) { d.Ci.RunUrl = "nope" }},
		{name: "a trigger kind is required", mutate: func(d *consolev1.Deployment) { d.Trigger.Kind = consolev1.TriggerKind_TRIGGER_KIND_UNSPECIFIED }},
		{name: "a failure message is bounded", mutate: func(d *consolev1.Deployment) { d.Error = strings.Repeat("x", 4001) }},
	})
}

func TestProtovalidateAcceptsEveryEnvironmentEventTheCLICanRecord(t *testing.T) {
	requireAccepted(t, "a removed preview", validEnvironmentEvent())

	destroyed := validEnvironmentEvent()
	destroyed.Kind = consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED
	destroyed.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}
	requireAccepted(t, "a destroyed production environment", destroyed)

	uncommitted := validEnvironmentEvent()
	uncommitted.Source = &consolev1.Source{Branch: "main"}
	requireAccepted(t, "a repository with no commits names only its branch", uncommitted)
}

func TestProtovalidateRefusesAnEnvironmentEventThatBreaksARule(t *testing.T) {
	requireRefusals(t, validEnvironmentEvent, []refusal[*consolev1.EnvironmentEvent]{
		{name: "the kind is required", mutate: func(e *consolev1.EnvironmentEvent) {
			e.Kind = consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_UNSPECIFIED
		}},
		{name: "the slug is required", mutate: func(e *consolev1.EnvironmentEvent) { e.Slug = "" }},
		{name: "the environment is required", mutate: func(e *consolev1.EnvironmentEvent) { e.Environment = nil }},
		{
			name: "the environment names its tier",
			mutate: func(e *consolev1.EnvironmentEvent) {
				e.Kind = consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED
				e.Environment.Tier = environmentv1.Tier_TIER_UNSPECIFIED
			},
			rule: "environment_event.environment_tier",
		},
		{name: "the time is required", mutate: func(e *consolev1.EnvironmentEvent) { e.At = nil }},
		{
			name: "a removed preview is in the preview tier",
			mutate: func(e *consolev1.EnvironmentEvent) {
				e.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}
			},
			rule: "environment_event.preview_removed_in_preview",
		},
		{
			name:   "a removed preview names the preview",
			mutate: func(e *consolev1.EnvironmentEvent) { e.Environment.Identity = "" },
			rule:   "environment_event.preview_removed_names_preview",
		},
		{name: "a commit is at least seven characters", mutate: func(e *consolev1.EnvironmentEvent) { e.Source = &consolev1.Source{Commit: "abc12"} }},
		{name: "a ci run names its ci", mutate: func(e *consolev1.EnvironmentEvent) { e.Ci = &consolev1.CI{} }},
	})
}

type recorder struct {
	consolev1connect.UnimplementedDeploymentServiceHandler
	deployments []*consolev1.Deployment
	events      []*consolev1.EnvironmentEvent
}

func (r *recorder) Report(_ context.Context, req *consolev1.ReportRequest) (*consolev1.ReportResponse, error) {
	r.deployments = append(r.deployments, req.GetDeployment())
	return &consolev1.ReportResponse{}, nil
}

func (r *recorder) RecordEnvironmentEvent(_ context.Context, req *consolev1.RecordEnvironmentEventRequest) (*consolev1.RecordEnvironmentEventResponse, error) {
	r.events = append(r.events, req.GetEvent())
	return &consolev1.RecordEnvironmentEventResponse{}, nil
}

func TestTheDeploymentServiceReceivesValidRecordsAndRefusesInvalidOnes(t *testing.T) {
	handler := &recorder{}
	mux := http.NewServeMux()
	mux.Handle(consolev1connect.NewDeploymentServiceHandler(handler, connect.WithInterceptors(connectvalidate.NewInterceptor())))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := consolev1connect.NewDeploymentServiceClient(server.Client(), server.URL)

	if _, err := client.Report(t.Context(), &consolev1.ReportRequest{Deployment: validDeployment()}); err != nil {
		t.Fatalf("reporting a valid deployment: %v", err)
	}
	if len(handler.deployments) != 1 || !proto.Equal(handler.deployments[0], validDeployment()) {
		t.Fatalf("the service received %v, want the reported deployment", handler.deployments)
	}

	_, err := client.Report(t.Context(), &consolev1.ReportRequest{Deployment: &consolev1.Deployment{}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("reporting an empty deployment: got %v, want invalid_argument", err)
	}
	if len(handler.deployments) != 1 {
		t.Fatal("an invalid deployment reached the handler")
	}

	if _, err := client.RecordEnvironmentEvent(t.Context(), &consolev1.RecordEnvironmentEventRequest{Event: validEnvironmentEvent()}); err != nil {
		t.Fatalf("recording a valid event: %v", err)
	}
	if len(handler.events) != 1 {
		t.Fatal("the event did not reach the handler")
	}

	_, err = client.RecordEnvironmentEvent(t.Context(), &consolev1.RecordEnvironmentEventRequest{})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("recording a missing event: got %v, want invalid_argument", err)
	}
}
