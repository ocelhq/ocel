package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"google.golang.org/api/googleapi"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

const (
	realtimeGatewayCPU         = "1"
	realtimeGatewayMemoryMiB   = 1024
	realtimeGatewayConcurrency = 1000
	realtimeGatewayMaxSockets  = 950
	realtimeGatewayInstances   = 1
	realtimeGatewayTimeout     = run.MaxRequestTimeout

	realtimeKeysVolume = "realtime-keys"
	realtimeKeysDir    = "/var/run/ocel/realtime"
	realtimeKeysFile   = "keys.json"

	realtimeImageName = "ocel-realtime"
	realtimeImagePath = "/realtime-gateway"
)

var realtimeImageTag = run.BinaryImageTag(payloads.RealtimeGateway)

func realtimeGatewayServing(named names.Names, ref provider.StackRef, host, image, inlineKeys string) run.Serving {
	gateway := run.Serving{
		Service:     named.RealtimeGateway(ref.Project, ref.Name.Env),
		Labels:      names.StackLabels(named, ref),
		Image:       image,
		Account:     named.RealtimeAccountEmail(ref.Tier),
		Compute:     provider.ComputeServerless,
		Public:      true,
		Ingress:     run.IngressEverywhere,
		CPU:         realtimeGatewayCPU,
		Memory:      realtimeGatewayMemoryMiB,
		Concurrency: realtimeGatewayConcurrency,
		Instances:   provider.Instances{Max: realtimeGatewayInstances},
		Timeout:     realtimeGatewayTimeout,
		Env: map[string]string{
			gatewayenv.HostVar:       host,
			gatewayenv.AnyOriginVar:  strconv.FormatBool(true),
			gatewayenv.MaxSocketsVar: strconv.Itoa(realtimeGatewayMaxSockets),
		},
	}
	if inlineKeys != "" {
		gateway.Env[gatewayenv.KeysVar] = inlineKeys
		return gateway
	}
	keys := run.SecretMount{Name: realtimeKeysVolume, Secret: named.RealtimeKeysSecret(ref.Tier, ref.Project, ref.Name.Env), Dir: realtimeKeysDir, File: realtimeKeysFile}
	gateway.Mounts = []run.SecretMount{keys}
	gateway.Env[gatewayenv.KeysFileVar] = keys.Dir + "/" + keys.File
	return gateway
}

func (r realtimeEnvironment) gatewayImage() string {
	return r.clients.RepositoryPath(r.clients.Region, r.ref.Tier) + "/" + realtimeImageName + ":" + realtimeImageTag()
}

func (r realtimeEnvironment) readGateway(ctx context.Context) (*runv2.GoogleCloudRunV2Service, error) {
	services, err := r.clients.Run()
	if err != nil {
		return nil, err
	}
	name := r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env)
	found, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*runv2.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(r.clients.ServicePath(name)).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the realtime gateway %s: %w", name, err)
	}
	return found, nil
}

func (r realtimeEnvironment) encodeInlineKeys(keys map[string]string) (string, error) {
	if !r.keysInEnv {
		return "", nil
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r realtimeEnvironment) findGatewayHost(current *runv2.GoogleCloudRunV2Service) string {
	if current != nil && run.HostOf(current.Uri) != "" {
		return run.HostOf(current.Uri)
	}
	return r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env)
}

func (r realtimeEnvironment) compareGateway(current *runv2.GoogleCloudRunV2Service, host, image, inlineKeys string) (run.Serving, string, error) {
	desired := realtimeGatewayServing(r.clients.Names, r.ref, host, image, inlineKeys)
	wanted, err := run.ServiceOf(desired)
	if err != nil {
		return run.Serving{}, "", err
	}
	switch {
	case current == nil:
		return desired, "the realtime gateway " + desired.Service + " is created", nil
	case imageOf(current) != image:
		return desired, "the realtime gateway " + desired.Service + " changes from image " + imageOf(current) + " to " + image, nil
	case run.Drifted(current, wanted):
		return desired, "the realtime gateway " + desired.Service + "'s Cloud Run settings differ from what ocel declares, so it is released again", nil
	case !run.ServesLatest(current):
		return desired, "the realtime gateway " + desired.Service + " does not serve its latest ready revision, so it is released again", nil
	}
	return desired, "", nil
}

func (r realtimeEnvironment) readGatewayDifference(ctx context.Context, keys map[string]string) (string, error) {
	inlineKeys, err := r.encodeInlineKeys(keys)
	if err != nil {
		return "", err
	}
	current, err := r.readGateway(ctx)
	if err != nil {
		return "", err
	}
	if current == nil {
		if err := r.refuseUnbootstrappedTier(ctx); err != nil {
			return "", err
		}
	}
	_, difference, err := r.compareGateway(current, r.findGatewayHost(current), r.gatewayImage(), inlineKeys)
	return difference, err
}

func (r realtimeEnvironment) serveGateway(ctx context.Context, keys map[string]string, progress progress.Log) (string, error) {
	inlineKeys, err := r.encodeInlineKeys(keys)
	if err != nil {
		return "", err
	}
	current, err := r.readGateway(ctx)
	if err != nil {
		return "", err
	}
	image := r.gatewayImage()
	if current == nil || imageOf(current) != image {
		if err := r.pushBinary(ctx, r.ref.Tier, realtimeImageName, image, payloads.RealtimeGateway(), realtimeImagePath); err != nil {
			return "", err
		}
	}
	if current == nil {
		if err := r.refuseUnbootstrappedTier(ctx); err != nil {
			return "", err
		}
	}
	name := r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env)
	host := r.findGatewayHost(current)
	for range 2 {
		desired, difference, err := r.compareGateway(current, host, image, inlineKeys)
		if err != nil {
			return "", err
		}
		if difference == "" {
			return host, nil
		}
		ran, err := r.deployAndRoute(ctx, desired, progress)
		if err != nil {
			return "", err
		}
		served := run.HostOf(ran.Url)
		if served == "" {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"Cloud Run gave the realtime gateway %s no URL, and a realtime binding names the host browsers connect to", name)
		}
		if served != host {
			host, current = served, nil
			continue
		}
		return host, nil
	}
	return "", refusal.Refuse(refusal.CodeBusy,
		"Cloud Run answered the realtime gateway %s on another host each time it was released, and a realtime binding names the one host browsers connect to\n"+
			"Deploy again once the service settles",
		name)
}

func (r realtimeEnvironment) refuseUnbootstrappedTier(ctx context.Context) error {
	accounts, err := r.clients.Accounts()
	if err != nil {
		return err
	}
	account := r.clients.RealtimeAccount(r.ref.Tier)
	_, err = googleretry.Attempted(ctx, accounts.Projects.ServiceAccounts.Get(gcloud.AccountPath(r.clients, account)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return refusal.Refuse(refusal.CodeNotReady,
			"the realtime gateways of tier %s run as the %s service account, and project %s has none: the tier was bootstrapped before realtime was served here.\n"+
				"Run `ocel bootstrap` for this tier again, then deploy",
			r.ref.Tier, account, r.clients.Project())
	}
	if err != nil {
		return fmt.Errorf("read the %s service account the realtime gateways of tier %s run as: %w", account, r.ref.Tier, err)
	}
	return nil
}

func imageOf(service *runv2.GoogleCloudRunV2Service) string {
	if service.Template == nil || len(service.Template.Containers) != 1 {
		return ""
	}
	return service.Template.Containers[0].Image
}

func (r realtimeEnvironment) removeGateway(ctx context.Context, progress progress.Log) error {
	return r.tearDown(ctx, r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env), progress)
}
