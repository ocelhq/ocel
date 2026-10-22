package cloudflare

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

var (
	testOrigin  = OriginScope{Vendor: "aws", ID: "123456789012/us-east-1"}
	otherOrigin = OriginScope{Vendor: "gcp", ID: "acme-shop"}
)

func fixedOrigin(origin OriginScope) Origin {
	return Origin{Scope: func(context.Context) (OriginScope, error) { return origin, nil }}
}

var scriptTagPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

func TestAnOriginsScriptTagReadsBackAsTheOriginItNames(t *testing.T) {
	t.Parallel()

	for _, origin := range []OriginScope{
		testOrigin,
		otherOrigin,
		{Vendor: "vps", ID: "k3j9x0"},
		{Vendor: "gcp", ID: "example.com:Shop_1"},
	} {
		tag := origin.scriptTag()
		if !scriptTagPattern.MatchString(tag) {
			t.Errorf("%s tags its scripts %q, want only lowercase letters, digits, dots, hyphens and underscores", origin, tag)
		}
		read, tagged := readOriginTag([]string{"unrelated", tag})
		if !tagged || read != origin {
			t.Errorf("readOriginTag(%q) = %v, %v, want %v", tag, read, tagged, origin)
		}
	}
}

func TestAnAWSOriginsScriptTagIsItsReadableOrigin(t *testing.T) {
	t.Parallel()

	if got, want := testOrigin.scriptTag(), "ocel-origin.aws.123456789012.us-east-1"; got != want {
		t.Errorf("scriptTag() = %q, want %q", got, want)
	}
	if got, want := testOrigin.String(), "aws:123456789012/us-east-1"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestATagNoOriginWroteReadsAsNoOrigin(t *testing.T) {
	t.Parallel()

	for _, tags := range [][]string{nil, {"production"}, {"ocel-origin."}, {"ocel-origin.aws"}, {"ocel-origin.aws..x"}, {"ocel-origin.aws.x_4"}, {"ocel-origin.aws.x_zz"}} {
		if read, tagged := readOriginTag(tags); tagged {
			t.Errorf("readOriginTag(%q) = %v, want no origin", tags, read)
		}
	}
}

func TestAnOriginThatNamesNoVendorOrNoIDNamesNothing(t *testing.T) {
	t.Parallel()

	for _, origin := range []OriginScope{{}, {Vendor: "aws"}, {ID: "123456789012/us-east-1"}} {
		if names, err := newCloudflare(defaultNamespace, fixedOrigin(origin), Options{}).resolveNames(t.Context()); err == nil {
			t.Errorf("names() of an edge opened for %v = %+v, want it refused: a name without its origin reaches another origin's store", origin, names)
		}
	}
	if _, err := newCloudflare(defaultNamespace, Origin{}, Options{}).resolveNames(t.Context()); err == nil {
		t.Error("names() of an edge opened with no origin = nil error, want it refused")
	}
}

func TestAnOriginThatCannotBeReadNamesNothing(t *testing.T) {
	t.Parallel()

	unread := errors.New("sts: no credentials")
	p := newCloudflare(defaultNamespace, Origin{Scope: func(context.Context) (OriginScope, error) { return OriginScope{}, unread }}, Options{})
	if _, err := p.ProjectOwner(t.Context(), "shop", environment.TierProduction); !errors.Is(err, unread) {
		t.Errorf("ProjectOwner() = %v, want the error reading the origin", err)
	}
}

func TestTwoOriginsNameEveryWorkerApart(t *testing.T) {
	t.Parallel()

	namesOf := func(origin originNames) []string {
		var names []string
		for _, tier := range workerTiers {
			store, err := origin.storeScript(tier)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := origin.entryWorker("shop", tier)
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, store, entry)
		}
		return append(names, origin.sharedPreviewEntry())
	}

	mine, theirs := namesOf(testNames), namesOf(otherNames)
	for _, name := range mine {
		if slices.Contains(theirs, name) {
			t.Errorf("%s and %s both name %q, so one's deploy or teardown reaches the other's", testOrigin, otherOrigin, name)
		}
	}
	for _, name := range mine {
		head := strings.SplitN(name, "-", 4)
		if len(head) < 4 || head[2] != testOrigin.tag() {
			t.Errorf("%q does not carry %s's tag %q right after its tier word, so a listing by prefix cannot find one origin's resources", name, testOrigin, testOrigin.tag())
		}
	}
}

func TestTearingOneOriginDownLeavesAnothersStanding(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	mine, err := testNames.storeScript(environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := otherNames.storeScript(environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	m := &cfMock{
		zoneID: "zone1", zoneName: "app.com",
		existingScripts: []string{mine, theirs},
	}

	if err := m.provider(t).Teardown(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	assertSet(t, "deleted scripts", m.deletedScripts, []string{mine})
}

func TestEveryWorkerAnOriginUploadsCarriesItsOrigin(t *testing.T) {
	t.Parallel()

	planned, err := testNames.bootstrapWorkers(environment.TierPreview)
	if err != nil {
		t.Fatal(err)
	}
	workers := map[string]workerScript{}
	for _, b := range planned {
		workers[b.scriptName] = b.worker
	}
	for _, slug := range []string{"shop", ""} {
		built, err := buildEntryProgram(testNames, environment.TierPreview, slug, previewProgrammed())
		if err != nil {
			t.Fatal(err)
		}
		workers[built.name] = built.worker
	}
	for name, worker := range workers {
		meta := metadataFromMultipart(t, worker, "")
		tags, _ := meta["tags"].([]any)
		if len(tags) != 1 || tags[0] != testOrigin.scriptTag() {
			t.Errorf("%s uploads with tags %v, want [%s]: a refusal can then name the origin that owns it", name, meta["tags"], testOrigin.scriptTag())
		}
	}
}

func TestAWorkersOriginReadsBackFromItsSettings(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	m := &cfMock{zoneID: "zone1", zoneName: "app.com", scriptSettings: map[string]map[string]any{
		"tagged":   {"tags": []any{"production", testOrigin.scriptTag()}},
		"untagged": {"tags": []any{"production"}},
	}}
	p := m.provider(t)

	if origin, tagged, err := p.readScriptOrigin(t.Context(), "acct", "tagged"); err != nil || !tagged || origin != testOrigin {
		t.Errorf("readScriptOrigin(tagged) = %v, %v, %v, want %v", origin, tagged, err, testOrigin)
	}
	if origin, tagged, err := p.readScriptOrigin(t.Context(), "acct", "untagged"); err != nil || tagged {
		t.Errorf("readScriptOrigin(untagged) = %v, %v, %v, want no origin", origin, tagged, err)
	}
	if _, tagged, err := p.readScriptOrigin(t.Context(), "acct", "missing"); err != nil || tagged {
		t.Errorf("readScriptOrigin(missing) = %v, %v, want no origin and no error", tagged, err)
	}
}
