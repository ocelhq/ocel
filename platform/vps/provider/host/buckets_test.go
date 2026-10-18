package host

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

func TestABucketThatIsNoLongerPublicHasItsAnonymousReadPolicyDeleted(t *testing.T) {
	t.Parallel()

	calls, err := aStore().calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	for _, call := range calls {
		if call.query != "policy" {
			continue
		}
		if call.method != http.MethodDelete {
			t.Fatalf("a private bucket's policy is driven with %s, so an anonymous-read policy a public deploy wrote keeps serving", call.method)
		}
		for _, absent := range []string{"204", "404"} {
			if !slices.Contains(call.allow, absent) {
				t.Errorf("deleting the policy refuses answer %s, so a bucket that never had one fails its deploy", absent)
			}
		}
		return
	}
	t.Fatal("a private bucket is described to the store with no policy call, so an anonymous-read policy a public deploy wrote keeps serving")
}

func aStore() BucketSpec {
	return BucketSpec{
		Store:       "shop-prod-store-s3",
		Tier:        "production",
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
	before, driven, cut := strings.Cut(script, "answered=$(")
	if !cut {
		t.Fatalf("the script drives the store without reading what it answered:\n%s", script)
	}
	_, tail, _ := strings.Cut(driven, "\n")
	for _, code := range append(create.allow, "500") {
		run := exec.Command("sh", "-c", before+"answered="+code+"\n"+tail)
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
				t.Errorf("%s runs curl without %s, so a store that stops answering stalls the deploy forever:\n%s", what, bound, script)
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

func heldAs(t *testing.T, spec BucketSpec) map[string]storeAnswer {
	t.Helper()
	answers := map[string]storeAnswer{
		"location": {code: "200"},
		"cors":     {code: "404"},
		"policy":   {code: "404"},
	}
	if cors, err := corsBody(spec.AllowedOrigins); err != nil {
		t.Fatal(err)
	} else if cors != nil {
		answers["cors"] = storeAnswer{code: "200", body: []byte(strings.Replace(string(cors),
			"<CORSConfiguration>", `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`, 1))}
	}
	if spec.Public {
		policy, err := anonymousReadPolicy(spec.Bucket)
		if err != nil {
			t.Fatal(err)
		}
		answers["policy"] = storeAnswer{code: "200", body: policy}
	}
	lifecycle, err := lifecycleBody()
	if err != nil {
		t.Fatal(err)
	}
	answers[lifecycleCall] = storeAnswer{code: "200", body: lifecycle}
	return answers
}

func plannedBucket(t *testing.T, spec BucketSpec, answers map[string]storeAnswer) (provider.ChangeAction, string) {
	t.Helper()
	action, reason, err := spec.planChange(answers)
	if err != nil {
		t.Fatalf("planChange() = %v", err)
	}
	return action, reason
}

func TestABucketTheStoreHoldsAsItsCallsLeaveItPlansKeep(t *testing.T) {
	t.Parallel()

	for _, public := range []bool{false, true} {
		spec := aStore()
		spec.Public = public
		spec.AllowedOrigins = []string{"https://app.example.com", "https://shop.example.com"}
		if action, reason := plannedBucket(t, spec, heldAs(t, spec)); action != provider.ActionKeep {
			t.Errorf("a bucket (public %t) the store holds exactly as its calls leave it plans %s %q, want keep", public, action, reason)
		}
	}
}

func TestABucketTheStoreDoesNotHavePlansACreate(t *testing.T) {
	t.Parallel()

	if action, reason := plannedBucket(t, aStore(), map[string]storeAnswer{"location": {code: "404"}}); action != provider.ActionCreate {
		t.Errorf("a bucket the store answers 404 for plans %s %q, want create", action, reason)
	}
}

func TestAPrivateBucketDeclaredPublicPlansBecomingReadableByAnyone(t *testing.T) {
	t.Parallel()

	held := heldAs(t, aStore())
	spec := aStore()
	spec.Public = true
	action, reason := plannedBucket(t, spec, held)
	if action != provider.ActionUpdate || !strings.Contains(reason, "becomes readable by anyone") {
		t.Errorf("a private bucket declared public plans %s %q, want an update saying it becomes readable by anyone", action, reason)
	}
}

func TestAPublicBucketDeclaredPrivatePlansNoLongerBeingReadableByAnyone(t *testing.T) {
	t.Parallel()

	public := aStore()
	public.Public = true
	action, reason := plannedBucket(t, aStore(), heldAs(t, public))
	if action != provider.ActionUpdate || !strings.Contains(reason, "stops being readable by anyone") {
		t.Errorf("a public bucket declared private plans %s %q, want an update saying it stops being readable by anyone", action, reason)
	}
}

func TestABucketWhoseOriginsChangePlansAnUpdateNamingBothSets(t *testing.T) {
	t.Parallel()

	was := aStore()
	was.AllowedOrigins = []string{"https://a.example.com"}
	spec := aStore()
	spec.AllowedOrigins = []string{"https://a.example.com", "https://b.example.com"}
	action, reason := plannedBucket(t, spec, heldAs(t, was))
	if action != provider.ActionUpdate || !strings.Contains(reason, "[https://a.example.com] to [https://a.example.com https://b.example.com]") {
		t.Errorf("a bucket gaining an origin plans %s %q, want an update naming both sets of origins", action, reason)
	}
}

func TestABucketLosingEveryOriginPlansAnUpdate(t *testing.T) {
	t.Parallel()

	was := aStore()
	was.AllowedOrigins = []string{"https://a.example.com"}
	action, reason := plannedBucket(t, aStore(), heldAs(t, was))
	if action != provider.ActionUpdate || !strings.Contains(reason, "https://a.example.com") {
		t.Errorf("a bucket losing its only origin plans %s %q, want an update naming the origin it stops answering", action, reason)
	}
}

func TestAPolicyTheStoreRendersInItsOwnShapeStillReadsAsAnonymousRead(t *testing.T) {
	t.Parallel()

	spec := aStore()
	spec.Public = true
	held := heldAs(t, spec)
	held["policy"] = storeAnswer{code: "200", body: []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::` + spec.Bucket + `/*"}]}`)}
	if action, reason := plannedBucket(t, spec, held); action != provider.ActionKeep {
		t.Errorf("an anonymous-read policy the store renders with bare strings plans %s %q, want keep", action, reason)
	}
}

func TestAPublicBucketWhosePolicyGrantsMoreThanReadsPlansReplacingIt(t *testing.T) {
	t.Parallel()

	spec := aStore()
	spec.Public = true
	held := heldAs(t, spec)
	held["policy"] = storeAnswer{code: "200", body: []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::` + spec.Bucket + `/*"}]}`)}
	if action, reason := plannedBucket(t, spec, held); action != provider.ActionUpdate || !strings.Contains(reason, "policy") {
		t.Errorf("a public bucket whose policy also lets anyone write plans %s %q, want an update replacing that policy", action, reason)
	}
}

func TestABucketMissingItsLifecyclePlansAnUpdate(t *testing.T) {
	t.Parallel()

	held := heldAs(t, aStore())
	held[lifecycleCall] = storeAnswer{code: "404"}
	if action, reason := plannedBucket(t, aStore(), held); action != provider.ActionUpdate || !strings.Contains(reason, "unfinished uploads") {
		t.Errorf("a bucket with no lifecycle plans %s %q, want an update saying unfinished uploads start expiring", action, reason)
	}
	held[lifecycleCall] = storeAnswer{code: "501"}
	if action, reason := plannedBucket(t, aStore(), held); action != provider.ActionKeep {
		t.Errorf("a bucket on a store that keeps no lifecycle plans %s %q, want keep: setting one is refused the same way", action, reason)
	}
}

func TestABucketOnAStoreThatIsNotRunningPlansSettingItsRulesAgain(t *testing.T) {
	t.Parallel()

	if action, reason := plannedBucket(t, aStore(), map[string]storeAnswer{}); action != provider.ActionUpdate || reason == "" {
		t.Errorf("a bucket whose store answered nothing plans %s %q, want an update with a reason", action, reason)
	}
}

func TestPlanningABucketOnlyReadsFromTheStore(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	spec := aStore()
	spec.Public = true
	if _, _, err := box.host().PlanBucket(context.Background(), spec); err != nil {
		t.Fatalf("PlanBucket() = %v", err)
	}
	asked := strings.Join(box.commands(), "\n")
	if !strings.Contains(asked, shell.Quote(http.MethodGet)) {
		t.Fatalf("planning a bucket asked the store nothing:\n%s", asked)
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPost} {
		if strings.Contains(asked, shell.Quote(method)) {
			t.Errorf("planning a bucket sent the store a %s:\n%s", method, asked)
		}
	}
}
