package server

import (
	"context"
	"errors"
	"testing"

	connect "connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func TestRunPruneRefusesAPreviewWithoutAPointer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  *deploymentsv1.Environment
	}{
		{"no identity at all", &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW}},
		{"a name the stack grammar cannot carry", &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "feature_login_ab12"}},
		{"production's own name", &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: deployEnv}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			touched := 0
			note := func(string) { touched++ }
			tracer := &fakeTracer{}
			stageReport := func(deploy.StageID) func(string) { return note }

			_, err := (&Server{}).runPrune(context.Background(),
				&deploymentsv1.RemoveStalePromotionsRequest{Slug: "shop", KeepN: 3, Environment: tc.env}, tracer, stageReport, note)

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
