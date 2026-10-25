package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"google.golang.org/api/serviceusage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	EnabledService           = "ENABLED"
	ServicesRole             = "roles/serviceusage.serviceUsageAdmin"
	servicesEnablePermission = "serviceusage.services.enable"
	servicesPerRead          = 30
	servicesPerSwitch        = 20
	failedPrecondition       = 9
	billingConsole           = "https://console.cloud.google.com/billing/linkedaccount?project="
	reasonSwitchedOff        = "switched off"

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
	if s == nil {
		return false, false
	}
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

func (b Engine) services() *ServiceStates {
	if b.ServiceStates != nil {
		return b.ServiceStates
	}
	return &ServiceStates{}
}

func (b Engine) ListServicesOff(ctx context.Context, apis []string) ([]string, error) {
	states := b.services()
	var wanted, unread []string
	for _, api := range apis {
		if slices.Contains(wanted, api) {
			continue
		}
		wanted = append(wanted, api)
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

func (b Engine) PartAPIs(tier environment.Tier, named []string) []string {
	var apis []string
	for _, each := range b.Removable {
		if slices.Contains(named, each.Name) {
			apis = append(apis, each.apis(b, tier)...)
		}
	}
	return apis
}

func (b Engine) TouchedAPIs(tier environment.Tier) []string {
	named := make([]string, 0, len(b.Removable))
	for _, each := range b.Removable {
		named = append(named, each.Name)
	}
	return uniqueAPIs(slices.Concat(BootstrapAPIs, b.PartAPIs(tier, named)))
}

func (b Engine) neededAPIs(req provider.BootstrapRequest) []string {
	parts := slices.Clone(req.Parts)
	if len(b.fronted(req.Parts)) > 0 {
		parts = append(parts, frontedParts...)
	}
	return uniqueAPIs(slices.Concat(BootstrapAPIs, b.PartAPIs(req.Tier, parts)))
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

func (b Engine) readTouchedServices(ctx context.Context, tier environment.Tier) error {
	_, err := b.ListServicesOff(ctx, b.TouchedAPIs(tier))
	return err
}

func (b Engine) preflight(ctx context.Context, req provider.BootstrapRequest) ([]string, error) {
	if err := b.readTouchedServices(ctx, req.Tier); err != nil {
		return nil, err
	}
	switching, err := b.ListServicesOff(ctx, b.neededAPIs(req))
	if err != nil {
		return nil, err
	}
	if err := b.RefuseServicesUnswitchable(ctx, switching); err != nil {
		return nil, err
	}
	return switching, b.Permitted(ctx, req.Parts)
}

func (b Engine) isOff(api string) bool { return b.ServiceStates.IsOff(api) }

func (b Engine) SwitchedOnGroup(apis []string) provider.ChangeGroup {
	return provider.ChangeGroup{
		Kind:    provider.StackGroupKind,
		Name:    string(gcloud.Vendor) + "/" + b.Clients.Project() + "/services",
		Action:  provider.ActionUpdate,
		Changes: switchedOn(apis),
	}
}

func switchedOn(apis []string) []provider.Change {
	changes := make([]provider.Change, 0, len(apis))
	for _, api := range apis {
		changes = append(changes, provider.Change{Kind: string(KindServiceUsage), Name: api, Action: provider.ActionUpdate, Reason: reasonSwitchedOff})
	}
	return changes
}

func (b Engine) RefuseServicesUnswitchable(ctx context.Context, off []string) error {
	if len(off) == 0 {
		return nil
	}
	granted, err := b.heldPermissions(ctx, []string{servicesEnablePermission})
	if err != nil || slices.Contains(granted, servicesEnablePermission) {
		return err
	}
	principal := "this credential"
	if b.Principal != nil {
		if principal, err = b.Principal(ctx); err != nil {
			return err
		}
	}
	project := b.Clients.Project()
	return refusal.Refuse(refusal.CodeDenied,
		"project %s has %s switched off, and %s may not switch services on there.\n"+
			"Run `gcloud services enable %s --project %s` once as someone who may, or grant %s %s on the project, then try again",
		project, strings.Join(off, ", "), principal, strings.Join(off, " "), project, principal, ServicesRole)
}

func (b Engine) SwitchOnServices(ctx context.Context, apis []string, progress progress.Log) error {
	off, err := b.ListServicesOff(ctx, apis)
	if err != nil || len(off) == 0 {
		return err
	}
	if err := b.RefuseServicesUnswitchable(ctx, off); err != nil {
		return err
	}
	return b.switchOn(ctx, off, progress)
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
		if err != nil {
			if refused := b.refuseUnbilled(batch, err.Error(), googleretry.AnsweredCode(err) == http.StatusBadRequest); refused != nil {
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
			if refused := b.refuseUnbilled(batch, finished.Error.Message, finished.Error.Code == failedPrecondition); refused != nil {
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

func (b Engine) refuseUnbilled(batch []string, message string, precondition bool) error {
	if !precondition || !strings.Contains(strings.ToLower(message), "billing") {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"project %s has no billing account linked, and Google switches on %s only in a project that bills.\n"+
			"Link one at %s%s, then try again",
		b.Clients.Project(), strings.Join(batch, ", "), billingConsole, url.QueryEscape(b.Clients.Project()))
}

func (b Engine) awaitServicesOn(ctx context.Context, apis []string) error {
	states := b.services()
	_, err := gcloud.Until(ctx, "Google reporting "+strings.Join(apis, ", ")+" on in project "+b.Clients.Project(), func() ([]string, error) {
		if err := b.readServices(ctx, states, apis); err != nil {
			return nil, err
		}
		return slices.DeleteFunc(slices.Clone(apis), func(api string) bool { return !states.IsOff(api) }), nil
	}, func(off []string) bool { return len(off) == 0 })
	return err
}
