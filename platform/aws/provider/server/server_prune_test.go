package server

import (
	"context"
	"errors"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func TestRunPruneRefusesAPreviewWithoutAPointer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  *environmentv1.Environment
	}{
		{"no identity at all", &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW}},
		{"a name the stack grammar cannot carry", &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "feature_login_ab12"}},
		{"production's own name", &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: deployEnv}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			touched := 0
			note := func(string) { touched++ }
			tracer := &fakeTracer{}
			stageReport := func(deploy.StageID) func(string) { return note }

			_, err := (&Server{}).runPrune(context.Background(),
				&contractv1.RemoveStalePromotionsRequest{Slug: "shop", KeepN: 3, Environment: tc.env}, tracer, stageReport, note)

			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("runPrune err = %v, want CodeInvalidArgument", err)
			}
			if touched != 0 {
				t.Errorf("progress/log calls = %d, want the request refused before anything is touched", touched)
			}
		})
	}
}
