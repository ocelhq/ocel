package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const kindScripted Kind = "test:scripted"

type scriptKey struct{}

type script func(ctx context.Context, i int) (Presence, error)

func init() {
	kinds[kindScripted] = KindOps{
		survey: func(ctx context.Context, _ Engine, _ Survey, target Resource) (Presence, error) {
			var i int
			if _, err := fmt.Sscan(target.Name, &i); err != nil {
				return Presence{}, err
			}
			return ctx.Value(scriptKey{}).(script)(ctx, i)
		},
	}
}

func newScriptedResources(n int) []Resource {
	items := make([]Resource, n)
	for i := range items {
		items[i] = Resource{Kind: kindScripted, Name: fmt.Sprint(i)}
	}
	return items
}

func surveyScripted(read Survey, items []Resource, answer script) error {
	ctx := context.WithValue(context.Background(), scriptKey{}, answer)
	return (Engine{ServiceStates: &ServiceStates{}}).surveyPresence(ctx, read, items)
}

func newEmptySurvey() Survey {
	return Survey{Tier: environment.TierProduction, Held: map[string]bool{}, Mending: map[string]string{}, Replacing: map[string]string{}}
}

const waitCeiling = 2 * time.Second

func TestASurveyReadsUpToEightResourcesAtOnce(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	reading, most := 0, 0
	full := make(chan struct{})
	var filled sync.Once
	answer := func(context.Context, int) (Presence, error) {
		mu.Lock()
		reading++
		most = max(most, reading)
		if reading == surveyReadsAtOnce {
			filled.Do(func() { close(full) })
		}
		mu.Unlock()
		select {
		case <-full:
		case <-time.After(waitCeiling):
		}
		mu.Lock()
		reading--
		mu.Unlock()
		return Presence{}, nil
	}

	if err := surveyScripted(newEmptySurvey(), newScriptedResources(20), answer); err != nil {
		t.Fatal(err)
	}
	if most != surveyReadsAtOnce {
		t.Errorf("the survey read %d resources at once, want %d", most, surveyReadsAtOnce)
	}
}

func TestASurveyRecordsWhatItReadsAsReadingThemOneAfterAnotherWouldWhateverOrderTheReadsFinishIn(t *testing.T) {
	t.Parallel()

	read := func(i int) Presence {
		switch i % 4 {
		case 1:
			return Presence{Present: true}
		case 2:
			return Presence{Present: true, Mends: fmt.Sprintf("resource %d lacks its tier's tag", i)}
		case 3:
			return Presence{Present: true, Replaces: fmt.Sprintf("resource %d moved", i)}
		}
		return Presence{}
	}
	items := newScriptedResources(surveyReadsAtOnce)
	finished := make([]chan struct{}, len(items))
	for i := range finished {
		finished[i] = make(chan struct{})
	}
	lastFirst := func(_ context.Context, i int) (Presence, error) {
		defer close(finished[i])
		if i+1 < len(items) {
			select {
			case <-finished[i+1]:
			case <-time.After(waitCeiling):
			}
		}
		return read(i), nil
	}

	want := newEmptySurvey()
	for i, item := range items {
		found := read(i)
		want.Held[item.ID()] = found.Present
		if found.Mends != "" {
			want.Mending[item.ID()] = found.Mends
		}
		if found.Replaces != "" {
			want.Replacing[item.ID()] = found.Replaces
		}
	}
	got := newEmptySurvey()
	if err := surveyScripted(got, items, lastFirst); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("survey = %+v, want %+v", got, want)
	}
}

func TestASurveyFailsWithTheErrorOfTheEarliestListedResourceItCouldNotRead(t *testing.T) {
	t.Parallel()

	errEarlier := errors.New("the earlier resource is unreadable")
	errLater := errors.New("the later resource is unreadable")
	laterFailed := make(chan struct{})
	answer := func(_ context.Context, i int) (Presence, error) {
		switch i {
		case 1:
			select {
			case <-laterFailed:
			case <-time.After(waitCeiling):
			}
			return Presence{}, errEarlier
		case 3:
			close(laterFailed)
			return Presence{}, errLater
		}
		return Presence{Present: true}, nil
	}

	err := surveyScripted(newEmptySurvey(), newScriptedResources(5), answer)
	if !errors.Is(err, errEarlier) {
		t.Errorf("the survey failed with %v, want the earlier resource's error %v", err, errEarlier)
	}
}

type unreadableTierBuckets struct {
	*fake.KeyValues
	tier environment.Tier
	err  error
}

func (s unreadableTierBuckets) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	if key.Partition.Tier == s.tier && key.Partition.Root == gcloud.RootTierBuckets {
		return records.Entry{}, s.err
	}
	return s.KeyValues.Read(ctx, key)
}

func TestASurveyThatCannotReadItsSiblingTiersBucketsNorAnyResourceFailsWithTheSiblingTiersError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"the caller may not read this resource"}}`))
	}))
	t.Cleanup(server.Close)
	errSibling := errors.New("the preview tier's buckets are unreadable")
	store := unreadableTierBuckets{KeyValues: fake.NewKeyValues(), tier: environment.TierPreview, err: errSibling}
	engine := Engine{ServiceStates: &ServiceStates{}, Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL, Records: store}}

	_, err := engine.survey(context.Background(), environment.TierProduction)
	if !errors.Is(err, errSibling) {
		t.Errorf("the survey failed with %v, want the sibling tier's error %v", err, errSibling)
	}
}
