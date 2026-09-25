package host

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func aStore() BucketSpec {
	return BucketSpec{
		Store:       "shop-prod-store-s3",
		Class:       "production",
		Endpoint:    "http://127.0.0.1:9000",
		Region:      "us-east-1",
		AccessKeyID: "ocel",
		SecretKey:   "s3cr3t",
		Bucket:      "shop-prod-uploads",
	}
}

func TestEveryCallTheStoreIsDrivenWithIsAScriptAShellWillRun(t *testing.T) {
	t.Parallel()

	spec := aStore()
	spec.Public = true
	spec.AllowedOrigins = []string{"https://app.example.com"}
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	if len(calls) < 4 {
		t.Fatalf("a public bucket is described to the store in %d calls", len(calls))
	}
	for _, call := range calls {
		req, err := spec.signed(call, time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("signed(%s) = %v", call.what, err)
		}
		script := curlCommand(spec.Store, req, call)
		read := exec.Command("sh", "-n")
		read.Stdin = strings.NewReader(script)
		if out, err := read.CombinedOutput(); err != nil {
			t.Errorf("the script that would %s is one no shell runs: %v\n%s\n%s",
				call.what, err, strings.TrimSpace(string(out)), script)
		}
	}
}

func TestAnAnswerTheCallAllowsIsNotAFailure(t *testing.T) {
	t.Parallel()

	spec := aStore()
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	create := calls[0]
	if len(create.allow) < 2 {
		t.Fatalf("creating a bucket allows %v, and a bucket that already exists is not a failure", create.allow)
	}
	req, err := spec.signed(create, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	script := curlCommand(spec.Store, req, create)
	held, driven, cut := strings.Cut(script, "answered=$(")
	if !cut {
		t.Fatalf("the script drives the store without reading what it answered:\n%s", script)
	}
	_, tail, _ := strings.Cut(driven, "\n")
	for _, code := range append(create.allow, "500") {
		run := exec.Command("sh", "-c", held+"answered="+code+"\n"+tail)
		out, err := run.CombinedOutput()
		if (code == "500") != (err != nil) {
			t.Errorf("the store answering %s exited %v: %s", code, err, strings.TrimSpace(string(out)))
		}
	}
}

func TestEveryCurlTheStoreIsDrivenWithGivesUpOnAStalledStore(t *testing.T) {
	t.Parallel()

	spec := aStore()
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	req, err := spec.signed(calls[0], time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for what, script := range map[string]string{
		"a bucket call":   curlCommand(spec.Store, req, calls[0]),
		"a store listing": storeScript(spec.Store, []*http.Request{req}, calls[:1]),
	} {
		for _, bound := range []string{"--connect-timeout", "--max-time"} {
			if !strings.Contains(script, bound) {
				t.Errorf("%s runs curl without %s, so a store that stops answering holds the deploy forever:\n%s", what, bound, script)
			}
		}
	}
}

func TestACurlThatGivesUpNamesTheStoreItWasDriving(t *testing.T) {
	t.Parallel()

	bin := t.TempDir()
	docker := "#!/bin/sh\ncat >/dev/null\necho 'curl: (28) Operation timed out after 60000 milliseconds' >&2\nexit 28\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := aStore()
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	req, err := spec.signed(calls[0], time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}

	run := exec.Command("sh", "-c", curlCommand(spec.Store, req, calls[0]))
	run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	run.Stdin = fedBody(calls[0].body)
	out, err := run.CombinedOutput()
	if err == nil {
		t.Fatal("the script passed though curl gave up")
	}
	for _, want := range []string{spec.Store, "timed out"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the script said %q, want it to name %q", strings.TrimSpace(string(out)), want)
		}
	}
}

func TestAStoreThatNeverAnsweredANamedCallIsNamedAsSilent(t *testing.T) {
	t.Parallel()

	spec := aStore()
	listing := listingCalls()[:1]
	_, err := droveStore(spec, listing, time.Unix(0, 0).UTC(), func(string, string) (string, error) {
		return listing[0].name + "=000\n", nil
	})
	if err == nil {
		t.Fatal("droveStore passed though the store never answered")
	}
	for _, want := range []string{spec.Store, "no answer", listing[0].what} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("droveStore = %v, want it to name %q", err, want)
		}
	}
}
