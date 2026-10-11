//go:build integration && cloudflarelive

package cloudflare

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	harnessRelease = "rlive"

	harnessReadyWithin = 3 * time.Minute

	purgeLandsWithin = 150 * time.Second

	stormRaisesAtMost = 150

	escalatedOnceFor = 40 * time.Second

	purgePollEvery = time.Second
)

type purgeHarness struct {
	url    string
	secret string
	client *http.Client
}

func TestLiveAPurgeQueueOnARealAccountPurgesWhatServeCached(t *testing.T) {
	h := newPurgeHarness(t)
	run := randomHex(t, 4)

	t.Run("a raise at the origin turns an edge entry into a MISS", func(t *testing.T) {
		tag := "watched-" + run
		before := h.cachedEntry(t, tag)

		h.raise(t, "purge", tag)

		h.waitForMiss(t, tag, before, purgeLandsWithin)
	})

	t.Run("a backlog older than 60 seconds escalates once, and a queue driven into error 1134 recovers", func(t *testing.T) {
		sentinel := "sentinel-" + run
		cleared := h.cachedEntry(t, sentinel)

		escalated := false
		for i := 0; i < stormRaisesAtMost && !escalated; i++ {
			h.raise(t, "purge", fmt.Sprintf("storm-%s-%03d", run, i))
			time.Sleep(purgePollEvery)
			body, _ := h.readEntry(t, sentinel)
			escalated = body != cleared
		}
		if !escalated {
			deadline := time.Now().Add(purgeLandsWithin)
			for !escalated && time.Now().Before(deadline) {
				time.Sleep(purgePollEvery)
				body, _ := h.readEntry(t, sentinel)
				escalated = body != cleared
			}
		}
		if !escalated {
			t.Fatalf("%s, raised by no one, was never cleared: %d raises a second never drove the queue past 60 seconds of backlog into purging everything", sentinel, stormRaisesAtMost)
		}

		held := h.cachedEntry(t, sentinel)
		for deadline := time.Now().Add(escalatedOnceFor); time.Now().Before(deadline); {
			time.Sleep(2 * purgePollEvery)
			if body, status := h.readEntry(t, sentinel); body != held {
				t.Fatalf("%s was cleared again (%s) within %s of the escalation, so the queue purged everything more than once for one backlog", sentinel, status, escalatedOnceFor)
			}
		}

		tag := "recovered-" + run
		before := h.cachedEntry(t, tag)
		h.raise(t, "purge", tag)
		h.waitForMiss(t, tag, before, purgeLandsWithin)
	})
}

func newPurgeHarness(t *testing.T) *purgeHarness {
	t.Helper()
	for _, name := range []string{"CLOUDFLARE_API_TOKEN", envAccountID} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is not set; this suite drives a real Cloudflare account", name)
		}
	}
	ctx := context.Background()
	p := newCloudflare("ocel", Options{})
	accountID := readAccountID()
	secret := randomHex(t, 32)
	up := upload{
		accountID:  accountID,
		scriptName: "ocel-purge-live-" + randomHex(t, 4),
		worker: edge.Worker{
			Main:              buildPurgeHarness(t),
			Variables:         map[string]string{"OCEL_HARNESS_RELEASE": harnessRelease, "OCEL_PURGE_SECRET": secret},
			CachedEntrypoints: []string{serveEntrypoint},
			DurableObjects:    []string{purgeQueueObject},
		},
	}
	t.Cleanup(func() {
		if err := p.deleteScript(context.Background(), accountID, up.scriptName); err != nil {
			t.Errorf("delete the harness worker %s: %v", up.scriptName, err)
		}
	})
	if err := p.putWorkerScript(ctx, up, "purge harness worker"); err != nil {
		t.Fatal(err)
	}
	url, err := p.setSubdomain(ctx, up, true)
	if err != nil {
		t.Fatalf("serve the harness on workers.dev: %v", err)
	}
	h := &purgeHarness{url: url, secret: secret, client: &http.Client{Timeout: 30 * time.Second}}
	h.waitUntilServing(t)
	return h
}

func buildPurgeHarness(t *testing.T) edge.WorkerModule {
	t.Helper()
	out := t.TempDir()
	entry, err := filepath.Abs(filepath.Join("..", "workers", "entry"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("pnpm", "exec", "wrangler", "deploy", "--dry-run", "--outdir", out, "--config", filepath.Join("test", "live", "wrangler.jsonc"))
	cmd.Dir = entry
	if said, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundle the purge harness: %v\n%s", err, said)
	}
	content, err := os.ReadFile(filepath.Join(out, "purge-harness.js"))
	if err != nil {
		t.Fatal(err)
	}
	return edge.WorkerModule{Name: "purge-harness.js", ContentType: "application/javascript+module", Content: content}
}

func randomHex(t *testing.T, bytes int) string {
	t.Helper()
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func (h *purgeHarness) waitUntilServing(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(harnessReadyWithin)
	for {
		res, err := h.client.Get(h.url + "/entry/ready")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the harness at %s did not answer within %s: %v", h.url, harnessReadyWithin, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func (h *purgeHarness) readEntry(t *testing.T, tag string) (body, status string) {
	t.Helper()
	res, err := h.client.Get(h.url + "/entry/" + tag)
	if err != nil {
		t.Fatalf("read %s: %v", tag, err)
	}
	defer res.Body.Close()
	read, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", tag, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("read %s answered %d: %s", tag, res.StatusCode, read)
	}
	return string(read), res.Header.Get("x-serve-cache")
}

func (h *purgeHarness) cachedEntry(t *testing.T, tag string) string {
	t.Helper()
	previous, _ := h.readEntry(t, tag)
	for deadline := time.Now().Add(purgeLandsWithin); time.Now().Before(deadline); time.Sleep(purgePollEvery) {
		body, _ := h.readEntry(t, tag)
		if body == previous {
			return body
		}
		previous = body
	}
	t.Fatalf("Serve never answered %s twice from its cache", tag)
	return ""
}

func (h *purgeHarness) waitForMiss(t *testing.T, tag, before string, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(purgePollEvery) {
		if body, status := h.readEntry(t, tag); body != before {
			t.Logf("%s rendered anew, Serve stamped %q", tag, status)
			return
		}
	}
	t.Fatalf("%s still answered from Serve's cache %s after it was raised; a refusal under the account's shared purge limit holds a purge for at most a minute and the backoff", tag, within)
}

func (h *purgeHarness) raise(t *testing.T, mode string, tags ...string) {
	t.Helper()
	raisedAt := time.Now().UnixMilli()
	type raisedTag struct {
		Tag      string `json:"tag"`
		RaisedAt int64  `json:"raisedAt"`
	}
	entries := make([]raisedTag, 0, len(tags))
	for _, tag := range tags {
		entries = append(entries, raisedTag{Tag: tag, RaisedAt: raisedAt})
	}
	body, err := json.Marshal(map[string]any{"release": harnessRelease, "mode": mode, "tags": entries})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, h.url+purgePath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-ocel-signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("raise %v: %v", tags, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		said, _ := io.ReadAll(res.Body)
		t.Fatalf("raise %v answered %d: %s", tags, res.StatusCode, said)
	}
}
