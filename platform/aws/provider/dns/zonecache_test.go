package dns

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRoute53RereadsTheZonesWhenNoneItRememberedOwnsTheName(t *testing.T) {
	t.Parallel()

	api := newFakeRoute53()
	writer := NewRoute53(api, "")
	first := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "front.example.net"}
	if _, err := writer.Ensure(t.Context(), []edge.Record{first}, nil); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if api.lists == 0 {
		t.Fatal("the zones were never listed, so nothing was remembered to go stale")
	}

	api.zones = append(api.zones, r53types.HostedZone{Id: aws.String("/hostedzone/Z-NEW"), Name: aws.String("new.example.")})
	listed := api.lists
	later := edge.Record{Name: "shop.new.example", Type: edge.RecordTypeCNAME, Value: "front.example.net"}
	if _, err := writer.Ensure(t.Context(), []edge.Record{later}, nil); err != nil {
		t.Fatalf("Ensure into a zone made since the first listing: %v", err)
	}
	if api.lists <= listed {
		t.Error("the zones were not listed again, so a zone made mid-run could never be written into")
	}
	if got := aws.ToString(api.changes[len(api.changes)-1].HostedZoneId); got != "Z-NEW" {
		t.Errorf("the record went into zone %q, want Z-NEW", got)
	}
}
