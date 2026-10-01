package ocel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ocel.dev"
)

type kvFixture struct {
	Keys []struct {
		Pattern string            `json:"pattern"`
		Params  map[string]string `json:"params"`
		Key     string            `json:"key"`
	} `json:"keys"`
	Values struct {
		Text    []struct{ Value, Stored string }
		Counter []struct {
			Value  int64
			Stored string
		}
		JSON []struct {
			Value  fixtureSession
			Stored string
		}
		List []struct{ Value, Stored []string }
		Set  []struct{ Value, Stored []string }
	} `json:"values"`
}

type fixtureSession struct {
	User   string   `json:"user"`
	Roles  []string `json:"roles"`
	Visits int      `json:"visits"`
	Note   string   `json:"note"`
}

func readKVFixture(t *testing.T) kvFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "proto", "app", "resources", "v1", "fixtures", "kv.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture kvFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestTheKVFixtureKeysAreBuiltAsEverySDKBuildsThem(t *testing.T) {
	ctx := context.Background()
	for i, tc := range readKVFixture(t).Keys {
		t.Run(tc.Pattern, func(t *testing.T) {
			name := fmt.Sprintf("keys%d", i)
			server := serveKV(t, name)
			cache := ocel.KV(name)
			var set func(value string) error
			var get func() (string, error)
			switch len(tc.Params) {
			case 0:
				entry := ocel.KVText[struct{}](cache, "entry", tc.Pattern)
				set = func(value string) error { return entry.Set(ctx, struct{}{}, value) }
				get = func() (string, error) { return entry.Get(ctx, struct{}{}) }
			case 1:
				var param string
				for _, value := range tc.Params {
					param = value
				}
				entry := ocel.KVText[string](cache, "entry", tc.Pattern)
				set = func(value string) error { return entry.Set(ctx, param, value) }
				get = func() (string, error) { return entry.Get(ctx, param) }
			default:
				key := memberKey{Room: tc.Params["room"], Member: tc.Params["member"]}
				entry := ocel.KVText[memberKey](cache, "entry", tc.Pattern)
				set = func(value string) error { return entry.Set(ctx, key, value) }
				get = func() (string, error) { return entry.Get(ctx, key) }
			}

			if err := set("written"); err != nil {
				t.Fatalf("Set() = %v", err)
			}
			if keys := server.Keys(); !slices.Equal(keys, []string{tc.Key}) {
				t.Errorf("keys = %q, want %q", keys, tc.Key)
			}
			_ = server.Set(tc.Key, "seeded")
			if got, err := get(); err != nil || got != "seeded" {
				t.Errorf("Get() = %q, %v, want the value seeded under %q", got, err, tc.Key)
			}
		})
	}
}
