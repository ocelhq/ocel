package dns

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeRoute53 struct {
	zones      []r53types.HostedZone
	live       map[string][]r53types.ResourceRecordSet
	changes    []*route53.ChangeResourceRecordSetsInput
	lists      int
	markerless bool
}

func (f *fakeRoute53) ListHostedZones(_ context.Context, in *route53.ListHostedZonesInput, _ ...func(*route53.Options)) (*route53.ListHostedZonesOutput, error) {
	f.lists++
	if f.markerless {
		return &route53.ListHostedZonesOutput{HostedZones: f.zones, IsTruncated: true}, nil
	}
	if aws.ToString(in.Marker) == "" && len(f.zones) > 1 {
		return &route53.ListHostedZonesOutput{
			HostedZones: f.zones[:1],
			IsTruncated: true,
			NextMarker:  aws.String("more"),
		}, nil
	}
	if aws.ToString(in.Marker) == "more" {
		return &route53.ListHostedZonesOutput{HostedZones: f.zones[1:]}, nil
	}
	return &route53.ListHostedZonesOutput{HostedZones: f.zones}, nil
}

func (f *fakeRoute53) ListResourceRecordSets(_ context.Context, in *route53.ListResourceRecordSetsInput, _ ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error) {
	return &route53.ListResourceRecordSetsOutput{ResourceRecordSets: f.live[aws.ToString(in.HostedZoneId)]}, nil
}

func (f *fakeRoute53) ChangeResourceRecordSets(_ context.Context, in *route53.ChangeResourceRecordSetsInput, _ ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error) {
	f.changes = append(f.changes, in)
	return &route53.ChangeResourceRecordSetsOutput{}, nil
}

func newFakeRoute53() *fakeRoute53 {
	return &fakeRoute53{
		zones: []r53types.HostedZone{
			{Id: aws.String("/hostedzone/Z-APP"), Name: aws.String("app.com.")},
			{Id: aws.String("/hostedzone/Z-PREVIEW"), Name: aws.String("preview.app.com.")},
		},
		live: map[string][]r53types.ResourceRecordSet{},
	}
}

func route53Name(name string) string {
	var out strings.Builder
	for i := 0; i < len(name); i++ {
		if c := name[i]; (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			out.WriteByte(c)
			continue
		}
		fmt.Fprintf(&out, `\%03o`, name[i])
	}
	out.WriteByte('.')
	return out.String()
}

func recordSetAt(name, value string) r53types.ResourceRecordSet {
	return r53types.ResourceRecordSet{
		Name:            aws.String(route53Name(name)),
		Type:            r53types.RRTypeCname,
		TTL:             aws.Int64(recordTTL),
		ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(value + ".")}},
	}
}

func TestRoute53EnsureRecords(t *testing.T) {
	t.Parallel()

	cname := edge.Record{Name: "*.preview.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"}

	t.Run("upserts into the longest matching hosted zone", func(t *testing.T) {
		t.Parallel()

		api := newFakeRoute53()
		if _, err := NewRoute53(api, "").EnsureRecords(t.Context(), []edge.Record{cname}, nil); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if len(api.changes) != 1 {
			t.Fatalf("changes = %v, want one", api.changes)
		}
		change := api.changes[0]
		if got := aws.ToString(change.HostedZoneId); got != "Z-PREVIEW" {
			t.Errorf("hosted zone = %q, want %q", got, "Z-PREVIEW")
		}
		set := change.ChangeBatch.Changes[0]
		if set.Action != r53types.ChangeActionUpsert {
			t.Errorf("action = %v, want %v", set.Action, r53types.ChangeActionUpsert)
		}
		if got := aws.ToString(set.ResourceRecordSet.ResourceRecords[0].Value); got != "front.example.net" {
			t.Errorf("value = %q, want %q", got, "front.example.net")
		}
	})

	t.Run("upserts into the named hosted zone", func(t *testing.T) {
		t.Parallel()

		api := newFakeRoute53()
		if _, err := NewRoute53(api, "app.com").EnsureRecords(t.Context(), []edge.Record{cname}, nil); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if got := aws.ToString(api.changes[0].HostedZoneId); got != "Z-APP" {
			t.Errorf("hosted zone = %q, want the named zone %q", got, "Z-APP")
		}
	})

	t.Run("names the hostname no hosted zone owns", func(t *testing.T) {
		t.Parallel()

		api := newFakeRoute53()
		_, err := NewRoute53(api, "").EnsureRecords(t.Context(), []edge.Record{{Name: "shop.elsewhere.com", Type: edge.RecordTypeCNAME, Value: "front"}}, nil)
		if err == nil {
			t.Fatal("EnsureRecords err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "shop.elsewhere.com") {
			t.Errorf("err = %q, want it to name the hostname", err)
		}
		if len(api.changes) != 0 {
			t.Errorf("changes = %v, want nothing written", api.changes)
		}
	})

	t.Run("refuses a proxied record it cannot express", func(t *testing.T) {
		t.Parallel()

		api := newFakeRoute53()
		proxied := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
		if _, err := NewRoute53(api, "").EnsureRecords(t.Context(), []edge.Record{proxied}, nil); err == nil {
			t.Fatal("EnsureRecords err = nil, want a refusal: Route 53 cannot proxy")
		}
	})

	t.Run("reads the hosted zones once, across pages", func(t *testing.T) {
		t.Parallel()

		api := newFakeRoute53()
		w := NewRoute53(api, "")
		for range 3 {
			if _, err := w.EnsureRecords(t.Context(), []edge.Record{cname}, nil); err != nil {
				t.Fatalf("EnsureRecords: %v", err)
			}
		}
		if api.lists != 2 {
			t.Errorf("hosted zone lists = %d, want 2: both pages read once", api.lists)
		}
	})

	t.Run("a truncated page naming no next marker ends the walk", func(t *testing.T) {
		t.Parallel()

		api := newFakeRoute53()
		api.markerless = true
		if _, err := NewRoute53(api, "").EnsureRecords(t.Context(), []edge.Record{cname}, nil); err != nil {
			t.Fatalf("EnsureRecords: %v", err)
		}
		if api.lists != 1 {
			t.Errorf("hosted zone lists = %d, want 1: a page with no marker cannot be followed", api.lists)
		}
	})
}

func TestRoute53DeleteRecords(t *testing.T) {
	t.Parallel()

	wildcard := edge.Record{Name: "*.preview.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"}
	plain := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"}

	cases := []struct {
		name    string
		written edge.Record
		zone    string
		live    []r53types.ResourceRecordSet
		want    bool
	}{
		{
			name:    "deletes a wildcard record Route 53 hands back escaped",
			written: wildcard,
			zone:    "Z-PREVIEW",
			live:    []r53types.ResourceRecordSet{recordSetAt("*.preview.app.com", "front.example.net")},
			want:    true,
		},
		{
			name:    "deletes a plain record whose value still matches",
			written: plain,
			zone:    "Z-APP",
			live:    []r53types.ResourceRecordSet{recordSetAt("shop.app.com", "front.example.net")},
			want:    true,
		},
		{
			name:    "leaves a record a human has repointed",
			written: wildcard,
			zone:    "Z-PREVIEW",
			live:    []r53types.ResourceRecordSet{recordSetAt("*.preview.app.com", "someone-elses.example.net")},
		},
		{
			name:    "leaves a name with nothing at it",
			written: wildcard,
			zone:    "Z-PREVIEW",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeRoute53()
			api.live[tc.zone] = tc.live
			if err := NewRoute53(api, "").DeleteRecords(t.Context(), []edge.Record{tc.written}); err != nil {
				t.Fatalf("DeleteRecords: %v", err)
			}
			if got := len(api.changes) == 1; got != tc.want {
				t.Errorf("deleted = %v, want %v (changes = %v)", got, tc.want, api.changes)
			}
			if !tc.want {
				return
			}
			if action := api.changes[0].ChangeBatch.Changes[0].Action; action != r53types.ChangeActionDelete {
				t.Errorf("action = %v, want %v", action, r53types.ChangeActionDelete)
			}
		})
	}
}

func TestWriterFor(t *testing.T) {
	t.Parallel()

	t.Run("no kind resolves to no writer", func(t *testing.T) {
		t.Parallel()

		writer, err := WriterFor("", "", Deps{})
		if err != nil || writer != nil {
			t.Errorf("WriterFor(\"\") = %v, %v, want nil, nil", writer, err)
		}
	})

	t.Run("an unknown kind names the ones this provider writes with", func(t *testing.T) {
		t.Parallel()

		_, err := WriterFor("bind9", "", Deps{})
		if err == nil {
			t.Fatal("WriterFor(\"bind9\") err = nil, want a refusal")
		}
		for _, kind := range SupportedKinds() {
			if !strings.Contains(err.Error(), kind) {
				t.Errorf("err = %q, want it to name %q", err, kind)
			}
		}
	})

	t.Run("route53 resolves to a writer", func(t *testing.T) {
		t.Parallel()

		writer, err := WriterFor(KindRoute53, "app.com", Deps{})
		if err != nil {
			t.Fatalf("WriterFor(route53) error = %v", err)
		}
		if writer == nil {
			t.Error("WriterFor(route53) = nil, want a writer")
		}
	})
}
