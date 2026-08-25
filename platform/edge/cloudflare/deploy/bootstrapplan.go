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
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/r2"
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
	reasonCurrent     = "already current"
	reasonScriptDrift = "the deployed script differs from this build's bundle"
)

var _ edge.BootstrapPlanner = (*provider)(nil)

func (p *provider) PlanBootstrap(ctx context.Context, class edge.Class) ([]edge.PlanChange, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return nil, err
	}
	changes, err := newCacheStore(p.client).plan(ctx, accountID, class)
	if err != nil {
		return nil, err
	}

	storeScript, err := storeScriptNameFor(class)
	if err != nil {
		return nil, err
	}
	storeWorker, err := storeWorkerBundle()
	if err != nil {
		return nil, err
	}
	storeChanges, err := p.planWorker(ctx, accountID, storeScript, storeWorker.Main)
	if err != nil {
		return nil, fmt.Errorf("plan deployments-store worker: %w", err)
	}

	writerScript, err := isrWriterScriptNameFor(class)
	if err != nil {
		return nil, err
	}
	writerWorker, err := isrWriterBundle()
	if err != nil {
		return nil, err
	}
	writerChanges, err := p.planWorker(ctx, accountID, writerScript, writerWorker.Main)
	if err != nil {
		return nil, fmt.Errorf("plan isr-writer worker: %w", err)
	}

	return append(append(changes, storeChanges...), writerChanges...), nil
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

func (p *provider) planWorker(ctx context.Context, accountID, scriptName string, main edge.WorkerModule) ([]edge.PlanChange, error) {
	state, err := p.readWorkerState(ctx, accountID, scriptName, main)
	if err != nil {
		return nil, err
	}
	return []edge.PlanChange{
		{Kind: kindWorker, Name: scriptName, Action: state.scriptAction(), Reason: state.scriptReason()},
		{Kind: kindWorkerSecret, Name: scriptName + "/" + bootstrapSecretBinding, Action: presence(state.secretHeld), Reason: keptReason(state.secretHeld)},
		{Kind: kindWorkerSubdomain, Name: scriptName, Action: presence(state.subdomainOn), Reason: keptReason(state.subdomainOn)},
	}, nil
}

func (s cacheStore) plan(ctx context.Context, accountID string, class edge.Class) ([]edge.PlanChange, error) {
	name, ok := cacheStoreNameByClass[class]
	if !ok {
		return nil, fmt.Errorf("cloudflare: unknown class %q", class)
	}
	bucketHeld, err := s.bucketPresent(ctx, accountID, name)
	if err != nil {
		return nil, err
	}
	_, tokenHeld, err := s.findToken(ctx, name)
	if err != nil {
		return nil, err
	}
	return []edge.PlanChange{
		{Kind: kindR2Bucket, Name: name, Action: presence(bucketHeld), Reason: keptReason(bucketHeld)},
		{Kind: kindAPIToken, Name: name, Action: presence(tokenHeld), Reason: keptReason(tokenHeld)},
	}, nil
}

func (s cacheStore) bucketPresent(ctx context.Context, accountID, name string) (bool, error) {
	_, err := s.buckets.Get(ctx, name, r2.BucketGetParams{AccountID: cf.F(accountID)})
	switch {
	case err == nil:
		return true, nil
	case hasStatus(err, http.StatusNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("look up R2 bucket %q: %w", name, err)
	}
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
	present     bool
	current     bool
	secretHeld  bool
	subdomainOn bool
}

func (w workerState) scriptAction() edge.PlanAction {
	switch {
	case !w.present:
		return edge.PlanCreate
	case !w.current:
		return edge.PlanUpdate
	default:
		return edge.PlanKeep
	}
}

func (w workerState) scriptReason() string {
	switch {
	case !w.present:
		return ""
	case !w.current:
		return reasonScriptDrift
	default:
		return reasonCurrent
	}
}

func (p *provider) readWorkerState(ctx context.Context, accountID, scriptName string, main edge.WorkerModule) (workerState, error) {
	deployed, present, err := p.deployedScript(ctx, accountID, scriptName, main.Name)
	if err != nil || !present {
		return workerState{}, err
	}
	state := workerState{present: true, current: bytes.Equal(deployed, main.Content)}

	secrets, err := p.client.Workers.Scripts.Secrets.List(ctx, scriptName, workers.ScriptSecretListParams{
		AccountID: cf.F(accountID),
	})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return workerState{}, fmt.Errorf("list the secrets of worker %q: %w", scriptName, err)
	}
	if secrets != nil {
		for _, secret := range secrets.Result {
			if secret.Name == bootstrapSecretBinding {
				state.secretHeld = true
			}
		}
	}

	subdomain, err := p.client.Workers.Scripts.Subdomain.Get(ctx, scriptName, workers.ScriptSubdomainGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return workerState{}, fmt.Errorf("read the workers.dev subdomain of worker %q: %w", scriptName, err)
	}
	if subdomain != nil {
		state.subdomainOn = subdomain.Enabled
	}
	return state, nil
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
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		if part.FormName() == moduleName || part.FileName() == moduleName {
			return content, nil
		}
	}
}
