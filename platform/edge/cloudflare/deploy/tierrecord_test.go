package cloudflare

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
)

func writeTierRecord(t *testing.T, store records.Store, tier environment.Tier, change func(*tierRecord)) {
	t.Helper()
	err := records.Change(context.Background(), store, tierRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		recorded, err := decodeTierRecord(entry)
		if err != nil {
			return nil, false, err
		}
		change(&recorded)
		value, err := json.Marshal(recorded)
		return value, true, err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readTierRecordOf(t *testing.T, store records.Store, tier environment.Tier) tierRecord {
	t.Helper()
	entry, err := records.ReadOrEmpty(context.Background(), store, tierRecordKey(tier))
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := decodeTierRecord(entry)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

func TestATeardownThatFailsKeepsTheTiersAccountAndZonesTogether(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com", refuseScriptDeletes: map[string]bool{sharedStoreScriptName: true}}
	zones := []recordedZone{{ID: "zone1", Name: "app.com"}}
	writeTierRecord(t, m.store(), environment.TierProduction, func(recorded *tierRecord) {
		recorded.AccountID = "acct"
		recorded.ClientCertificateZones = zones
	})

	if err := m.provider(t).Teardown(t.Context(), environment.TierProduction); err == nil {
		t.Fatal("Teardown err = nil, want the refused delete reported")
	}
	if got := readTierRecordOf(t, m.store(), environment.TierProduction); got.AccountID != "acct" || !slices.Equal(got.ClientCertificateZones, zones) {
		t.Errorf("tier record = %+v after a failed teardown, want account acct and zones %v kept together for the next run", got, zones)
	}
}
