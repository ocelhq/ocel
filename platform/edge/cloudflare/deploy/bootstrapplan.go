package cloudflare

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	kindR2Bucket        = "Cloudflare::R2Bucket"
	kindAPIToken        = "Cloudflare::APIToken"
	kindWorker          = "Cloudflare::Worker"
	kindWorkerSecret    = "Cloudflare::WorkerSecret"
	kindWorkerSubdomain = "Cloudflare::WorkerSubdomain"
)

const (
	reasonCurrent       = "already current"
	reasonScriptDrift   = "the deployed script differs from this build's bundle"
	reasonMetadataDrift = "the deployed worker's compatibility settings or bindings differ from this build's"
)

var (
	_ edge.BootstrapPlanner = (*provider)(nil)
	_ edge.BootstrapAdopter = (*provider)(nil)
)

func (p *provider) PlanBootstrap(ctx context.Context, class edge.Class) ([]edge.PlanChange, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return nil, err
	}
	state, err := p.readState(ctx, accountID, class)
	if err != nil {
		return nil, err
	}
	return state.changes(), nil
}

func bootstrapCredentials() (string, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; export it and re-run", envAccountID)
	}
	if os.Getenv(envAPIToken) == "" {
		return "", fmt.Errorf("%s is not set; export it and re-run", envAPIToken)
	}
	return accountID, nil
}

type bootstrapState struct {
	store   cacheStoreState
	workers []workerState
}

func (p *provider) readState(ctx context.Context, accountID string, class edge.Class) (bootstrapState, error) {
	store, err := newCacheStore(p.client).read(ctx, accountID, class)
	if err != nil {
		return bootstrapState{}, err
	}
	planned, err := bootstrapWorkers(class)
	if err != nil {
		return bootstrapState{}, err
	}
	state := bootstrapState{store: store}
	for _, b := range planned {
		read, err := p.readWorkerState(ctx, accountID, b)
		if err != nil {
			return bootstrapState{}, fmt.Errorf("read %s: %w", b.what, err)
		}
		state.workers = append(state.workers, read)
	}
	return state, nil
}

func (s bootstrapState) changes() []edge.PlanChange {
	changes := []edge.PlanChange{
		{Kind: kindR2Bucket, Name: s.store.name, Action: presence(s.store.bucketHeld), Reason: keptReason(s.store.bucketHeld)},
		{Kind: kindAPIToken, Name: s.store.name, Action: presence(s.store.tokenHeld), Reason: keptReason(s.store.tokenHeld)},
	}
	for _, worker := range s.workers {
		changes = append(changes, worker.changes()...)
	}
	return changes
}

func presence(held bool) edge.PlanAction {
	if held {
		return edge.PlanKeep
	}
	return edge.PlanCreate
}

func keptReason(held bool) string {
	if held {
		return reasonCurrent
	}
	return ""
}

type workerState struct {
	bootstrapWorker
	present         bool
	scriptCurrent   bool
	metadataCurrent bool
	secretHeld      bool
	subdomainOn     bool
	classes         []string
}

func (w workerState) changes() []edge.PlanChange {
	name := w.scriptName
	return []edge.PlanChange{
		{Kind: kindWorker, Name: name, Action: w.scriptAction(), Reason: w.scriptReason()},
		{Kind: kindWorkerSecret, Name: name + "/" + bootstrapSecretBinding, Action: presence(w.secretHeld), Reason: keptReason(w.secretHeld)},
		{Kind: kindWorkerSubdomain, Name: name, Action: presence(w.subdomainOn), Reason: keptReason(w.subdomainOn)},
	}
}

func (w workerState) settled() bool {
	return w.present && w.scriptCurrent && w.metadataCurrent
}

func (w workerState) scriptAction() edge.PlanAction {
	switch {
	case !w.present:
		return edge.PlanCreate
	case !w.settled():
		return edge.PlanUpdate
	default:
		return edge.PlanKeep
	}
}

func (w workerState) scriptReason() string {
	switch {
	case !w.present:
		return ""
	case !w.scriptCurrent:
		return reasonScriptDrift
	case !w.metadataCurrent:
		return reasonMetadataDrift
	default:
		return reasonCurrent
	}
}

func (p *provider) readWorkerState(ctx context.Context, accountID string, b bootstrapWorker) (workerState, error) {
	deployed, present, err := p.deployedScript(ctx, accountID, b.scriptName, b.worker.Main.Name)
	if err != nil || !present {
		return workerState{bootstrapWorker: b}, err
	}
	state := workerState{bootstrapWorker: b, present: true, scriptCurrent: bytes.Equal(deployed, b.worker.Main.Content)}

	settings, err := p.scriptSettings(ctx, accountID, b.scriptName)
	if err != nil {
		return workerState{}, err
	}
	state.metadataCurrent = settingsCurrent(settings, b)
	state.classes = deployedClasses(settings)

	secrets, err := p.client.Workers.Scripts.Secrets.List(ctx, b.scriptName, workers.ScriptSecretListParams{
		AccountID: cf.F(accountID),
	})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return workerState{}, fmt.Errorf("list the secrets of worker %q: %w", b.scriptName, err)
	}
	if secrets != nil {
		state.secretHeld = slices.ContainsFunc(secrets.Result, func(secret workers.ScriptSecretListResponse) bool {
			return secret.Name == bootstrapSecretBinding
		})
	}

	subdomain, err := p.client.Workers.Scripts.Subdomain.Get(ctx, b.scriptName, workers.ScriptSubdomainGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return workerState{}, fmt.Errorf("read the workers.dev subdomain of worker %q: %w", b.scriptName, err)
	}
	if subdomain != nil {
		state.subdomainOn = subdomain.Enabled
	}
	return state, nil
}

func (p *provider) scriptSettings(ctx context.Context, accountID, scriptName string) (*workers.ScriptScriptAndVersionSettingGetResponse, error) {
	settings, err := p.client.Workers.Scripts.ScriptAndVersionSettings.Get(ctx, scriptName, workers.ScriptScriptAndVersionSettingGetParams{
		AccountID: cf.F(accountID),
	})
	if hasStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the settings of worker %q: %w", scriptName, err)
	}
	return settings, nil
}

func settingsCurrent(settings *workers.ScriptScriptAndVersionSettingGetResponse, b bootstrapWorker) bool {
	if settings == nil {
		return false
	}
	if settings.CompatibilityDate != compatDate {
		return false
	}
	deployed := slices.Clone(settings.CompatibilityFlags)
	slices.Sort(deployed)
	wanted := slices.Clone(compatFlags)
	slices.Sort(wanted)
	if !slices.Equal(deployed, wanted) {
		return false
	}
	held := deployedBindings(settings)
	for _, want := range b.bindings() {
		if !slices.Contains(held, want) {
			return false
		}
	}
	return true
}

type binding struct {
	kind  string
	name  string
	value string
}

func (b bootstrapWorker) bindings() []binding {
	var wanted []binding
	for _, declared := range scriptBindings(b.worker, false) {
		if ref, ok := comparableBinding(fmt.Sprint(declared["type"]), declared); ok {
			wanted = append(wanted, ref)
		}
	}
	for _, class := range b.do.classes {
		wanted = append(wanted, binding{kind: durableObjectBindingType, name: class.binding, value: class.className})
	}
	return wanted
}

func comparableBinding(kind string, declared map[string]any) (binding, bool) {
	ref := binding{kind: kind, name: fmt.Sprint(declared["name"])}
	switch kind {
	case "r2_bucket":
		ref.value = fmt.Sprint(declared["bucket_name"])
	case "service":
		ref.value = fmt.Sprint(declared["service"])
	case "plain_text":
		ref.value = fmt.Sprint(declared["text"])
	case "worker_loader":
	default:
		return binding{}, false
	}
	return ref, true
}

func deployedBindings(settings *workers.ScriptScriptAndVersionSettingGetResponse) []binding {
	var held []binding
	for _, deployed := range settings.Bindings {
		ref := binding{kind: string(deployed.Type), name: deployed.Name}
		switch ref.kind {
		case "r2_bucket":
			ref.value = deployed.BucketName
		case "service":
			ref.value = deployed.Service
		case "plain_text":
			ref.value = deployed.Text
		case durableObjectBindingType:
			ref.value = deployed.ClassName
		case "worker_loader":
		default:
			continue
		}
		held = append(held, ref)
	}
	return held
}

func deployedClasses(settings *workers.ScriptScriptAndVersionSettingGetResponse) []string {
	if settings == nil {
		return nil
	}
	var classes []string
	for _, deployed := range settings.Bindings {
		if string(deployed.Type) == durableObjectBindingType && deployed.ClassName != "" && deployed.ScriptName == "" {
			classes = append(classes, deployed.ClassName)
		}
	}
	return classes
}

func (p *provider) deployedScript(ctx context.Context, accountID, scriptName, moduleName string) ([]byte, bool, error) {
	res, err := p.client.Workers.Scripts.Content.Get(ctx, scriptName, workers.ScriptContentGetParams{
		AccountID: cf.F(accountID),
	})
	if hasStatus(err, http.StatusNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read the content of worker %q: %w", scriptName, err)
	}
	content, err := moduleContent(res, moduleName)
	if err != nil {
		return nil, false, fmt.Errorf("read the content of worker %q: %w", scriptName, err)
	}
	return content, true, nil
}

func moduleContent(res *http.Response, moduleName string) ([]byte, error) {
	defer res.Body.Close()
	mediaType, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return io.ReadAll(res.Body)
	}
	mr := multipart.NewReader(res.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, fmt.Errorf("no part is named %q; the deployed script carries modules this build does not", moduleName)
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() != moduleName && part.FileName() != moduleName {
			continue
		}
		return io.ReadAll(part)
	}
}
