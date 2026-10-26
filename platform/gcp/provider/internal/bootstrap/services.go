package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/serviceusage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	EnabledService    = "ENABLED"
	ServicesRole      = "roles/serviceusage.serviceUsageAdmin"
	servicesPerRead   = 30
	servicesPerSwitch = 20
	billingConsole    = "https://console.cloud.google.com/billing/linkedaccount?project="
	reasonSwitchedOff = "switched off"
	billingNotFound   = "UREQ_PROJECT_BILLING_NOT_FOUND"
	errorInfoType     = "type.googleapis.com/google.rpc.ErrorInfo"

	KindServiceUsage Kind = "serviceusage:service"
)

type ServiceStates struct {
	mu sync.Mutex
	on map[string]bool
}

func (s *ServiceStates) IsOff(api string) bool {
	on, known := s.recall(api)
	return known && !on
}

func (s *ServiceStates) recall(api string) (on, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	on, known = s.on[api]
	return on, known
}

func (s *ServiceStates) remember(api string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.on == nil {
		s.on = map[string]bool{}
	}
	s.on[api] = on
}

func (b Engine) ListServicesOff(ctx context.Context, apis []string) ([]string, error) {
	states := b.ServiceStates
	wanted := uniqueAPIs(apis)
	var unread []string
	for _, api := range wanted {
		if _, known := states.recall(api); !known {
			unread = append(unread, api)
		}
	}
	if err := b.readServices(ctx, states, unread); err != nil {
		return nil, err
	}
	var off []string
	for _, api := range wanted {
		if states.IsOff(api) {
			off = append(off, api)
		}
	}
	return off, nil
}

func (b Engine) readServices(ctx context.Context, states *ServiceStates, apis []string) error {
	if len(apis) == 0 {
		return nil
	}
	service, err := b.Clients.Services()
	if err != nil {
		return err
	}
	parent := "projects/" + b.Clients.Project()
	for batch := range slices.Chunk(apis, servicesPerRead) {
		names := make([]string, len(batch))
		for i, api := range batch {
			names[i] = parent + "/services/" + api
		}
		read, err := googleretry.Attempted(ctx, service.Services.BatchGet(parent).Names(names...).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("read whether %s are on in project %s: %w", strings.Join(batch, ", "), b.Clients.Project(), err)
		}
		for _, api := range batch {
			states.remember(api, false)
		}
		for _, each := range read.Services {
			states.remember(serviceID(each.Name), each.State == EnabledService)
		}
	}
	return nil
}

func serviceID(name string) string { return name[strings.LastIndex(name, "/")+1:] }

func (r RemovablePart) apis(b Engine, tier environment.Tier) []string {
	if r.Deployed != nil {
		return r.Deployed(b, tier).APIs
	}
	return r.APIs
}

func (b Engine) partAPIs(tier environment.Tier, named []string) []string {
	var apis []string
	for _, each := range b.Removable {
		if slices.Contains(named, each.Name) {
			apis = append(apis, each.apis(b, tier)...)
		}
	}
	return apis
}

func (b Engine) touchedAPIs(tier environment.Tier) []string {
	apis := slices.Clone(BootstrapAPIs)
	for _, each := range b.Removable {
		apis = append(apis, each.apis(b, tier)...)
	}
	return apis
}

func (b Engine) ReadServices(ctx context.Context, tier environment.Tier) error {
	if b.SkipChecks {
		return nil
	}
	_, err := b.ListServicesOff(ctx, b.touchedAPIs(tier))
	return err
}

func uniqueAPIs(apis []string) []string {
	var unique []string
	for _, api := range apis {
		if !slices.Contains(unique, api) {
			unique = append(unique, api)
		}
	}
	return unique
}

func (b Engine) ServicesToSwitchOn(ctx context.Context, tier environment.Tier, parts, apis []string) ([]string, error) {
	needed := slices.Concat(apis, b.partAPIs(tier, parts))
	if len(needed) == 0 {
		return nil, nil
	}
	if _, err := b.ListServicesOff(ctx, slices.Concat(b.touchedAPIs(tier), needed)); err != nil {
		return nil, err
	}
	switching := slices.DeleteFunc(uniqueAPIs(needed), func(api string) bool { return !b.IsOff(api) })
	return switching, b.RefuseSwitchOnDenied(ctx, switching)
}

func (b Engine) SwitchOnServices(ctx context.Context, tier environment.Tier, parts, apis []string, progress progress.Log) error {
	switching, err := b.ServicesToSwitchOn(ctx, tier, parts, apis)
	if err != nil {
		return err
	}
	if err := b.switchOn(ctx, switching, progress); err != nil {
		return err
	}
	return b.refuseRunRegionUnserved(ctx)
}

func (b Engine) RefuseServicesOff(ctx context.Context, apis []string) error {
	if b.SkipChecks {
		return nil
	}
	off, err := b.ListServicesOff(ctx, apis)
	if err != nil || len(off) == 0 {
		return err
	}
	project := b.Clients.Project()
	return refusal.Refuse(refusal.CodeNotReady, "project %s has %s switched off.\nRun `gcloud services enable %s --project %s`, then try again",
		project, strings.Join(off, ", "), strings.Join(off, " "), project)
}

func (b Engine) IsOff(api string) bool { return b.ServiceStates.IsOff(api) }

func (b Engine) PlanSwitchOn(switching []string) []provider.ChangeGroup {
	if len(switching) == 0 {
		return nil
	}
	changes := make([]provider.Change, 0, len(switching))
	for _, api := range switching {
		changes = append(changes, provider.Change{Kind: string(KindServiceUsage), Name: api, Action: provider.ActionUpdate, Reason: reasonSwitchedOff})
	}
	return []provider.ChangeGroup{{
		Kind:    provider.StackGroupKind,
		Name:    string(gcloud.Vendor) + "/" + b.Clients.Project() + "/services",
		Action:  provider.ActionUpdate,
		Changes: changes,
	}}
}

func (b Engine) RefuseSwitchOnDenied(ctx context.Context, off []string) error {
	if len(off) == 0 || b.IsOff(resourceManagerAPI) {
		return nil
	}
	granted, err := b.heldPermissions(ctx, credential.SwitchOnPermissions)
	if err != nil || !slices.ContainsFunc(credential.SwitchOnPermissions, func(permission string) bool { return !slices.Contains(granted, permission) }) {
		return err
	}
	return b.refuseSwitchOn(ctx, off)
}

func (b Engine) refuseSwitchOn(ctx context.Context, off []string) error {
	principal, err := b.Principal(ctx)
	if err != nil {
		return err
	}
	project := b.Clients.Project()
	return refusal.Refuse(refusal.CodeDenied,
		"%s may not switch on %s in project %s.\n"+
			"Have someone who may run `gcloud services enable %s --project %s`, then try again",
		principal, strings.Join(off, ", "), project, strings.Join(off, " "), project)
}

func (b Engine) switchOn(ctx context.Context, off []string, progress progress.Log) error {
	if len(off) == 0 {
		return nil
	}
	service, err := b.Clients.Services()
	if err != nil {
		return err
	}
	parent := "projects/" + b.Clients.Project()
	for batch := range slices.Chunk(off, servicesPerSwitch) {
		doing := "switching on " + strings.Join(batch, ", ") + " in project " + b.Clients.Project()
		started, err := googleretry.Attempted(ctx, service.Services.BatchEnable(parent, &serviceusage.BatchEnableServicesRequest{ServiceIds: batch}).Context(ctx).Do)
		if gcloud.Forbidden(err) && !gcloud.ServiceDisabled(err) {
			return b.refuseSwitchOn(ctx, off)
		}
		if err != nil {
			if refused := b.refuseUnbilled(gcloud.AnsweredReason(err) == billingNotFound); refused != nil {
				return refused
			}
			return fmt.Errorf("%s: %w", doing, err)
		}
		finished, err := gcloud.Until(ctx, doing, func() (*serviceusage.Operation, error) {
			if started.Done {
				return started, nil
			}
			return googleretry.Attempted(ctx, service.Operations.Get(started.Name).Context(ctx).Do)
		}, func(polled *serviceusage.Operation) bool { return polled.Done })
		if err != nil {
			return err
		}
		if finished.Error != nil {
			if refused := b.refuseUnbilled(slices.ContainsFunc(finished.Error.Details, isBillingNotFound)); refused != nil {
				return refused
			}
			return fmt.Errorf("%s: %s", doing, finished.Error.Message)
		}
	}
	if err := b.awaitServicesOn(ctx, off); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Switched on " + strings.Join(off, ", ") + " in project " + b.Clients.Project())
	return nil
}

func isBillingNotFound(detail googleapi.RawMessage) bool {
	var info struct {
		Type   string `json:"@type"`
		Reason string `json:"reason"`
	}
	return json.Unmarshal(detail, &info) == nil && info.Type == errorInfoType && info.Reason == billingNotFound
}

func (b Engine) refuseUnbilled(unbilled bool) error {
	if !unbilled {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"project %s has no billing account linked.\nLink one at %s%s, then try again",
		b.Clients.Project(), billingConsole, url.QueryEscape(b.Clients.Project()))
}

func (b Engine) awaitServicesOn(ctx context.Context, apis []string) error {
	states := b.ServiceStates
	_, err := gcloud.Until(ctx, "Google reporting "+strings.Join(apis, ", ")+" on in project "+b.Clients.Project(), func() ([]string, error) {
		if err := b.readServices(ctx, states, apis); err != nil {
			return nil, err
		}
		return slices.DeleteFunc(slices.Clone(apis), func(api string) bool { return !states.IsOff(api) }), nil
	}, func(off []string) bool { return len(off) == 0 })
	return err
}
