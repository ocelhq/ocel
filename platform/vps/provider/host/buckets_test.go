package host

import (
	"os/exec"
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
	held, _, cut := strings.Cut(script, "answered=$(")
	if !cut {
		t.Fatalf("the script drives the store without reading what it answered:\n%s", script)
	}
	_, tail, _ := strings.Cut(script, ")\n")
	for _, code := range append(create.allow, "500") {
		run := exec.Command("sh", "-c", held+"answered="+code+"\n"+tail)
		out, err := run.CombinedOutput()
		if (code == "500") != (err != nil) {
			t.Errorf("the store answering %s exited %v: %s", code, err, strings.TrimSpace(string(out)))
		}
	}
}
