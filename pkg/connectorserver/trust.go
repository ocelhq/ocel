package connectorserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/consoletoken"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

type trust struct {
	connectorID string
	subject     string
	grants      []string
	console     *consoletoken.Issuer
}

func newTrust(spec Spec) (*trust, error) {
	origin, err := originOf(spec.Console)
	if err != nil {
		return nil, err
	}
	return &trust{
		connectorID: spec.ConnectorID,
		subject:     "org:" + spec.OrganizationID,
		grants:      spec.Grants,
		console:     consoletoken.NewIssuer(origin),
	}, nil
}

func (t *trust) interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := request.Spec().Procedure
			caller, err := t.caller(ctx, request.Header().Get("Authorization"))
			if err != nil {
				t.say("-", procedure, "denied")
				return nil, err
			}
			want, mapped := scopeOf(procedure, request.Any())
			if !mapped {
				t.say(caller.act, procedure, "denied")
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("no scope maps %s", procedure))
			}
			if !slices.Contains(t.grants, want) {
				t.say(caller.act, procedure, "denied")
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("this connector has no %s grant", want))
			}
			if !slices.Contains(caller.scopes, want) {
				t.say(caller.act, procedure, "denied")
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("the token has no %s scope", want))
			}
			t.say(caller.act, procedure, "pass")
			return next(ctx, request)
		}
	})
}

func (t *trust) say(act, procedure, outcome string) {
	fmt.Printf("act=%s org=%s procedure=%s outcome=%s\n",
		or(act, "-"), strings.TrimPrefix(t.subject, "org:"), procedure, outcome)
}

func or(named, fallback string) string {
	if named == "" {
		return fallback
	}
	return named
}

type claims struct {
	act    string
	scopes []string
}

func (t *trust) caller(ctx context.Context, authorization string) (claims, error) {
	token, err := t.console.Verify(ctx, authorization, t.connectorID)
	if errors.Is(err, consoletoken.ErrNoScope) {
		return claims{}, connect.NewError(connect.CodePermissionDenied, err)
	}
	if err != nil {
		return claims{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if token.Subject != t.subject {
		return claims{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("the token speaks for %q, not for this account", token.Subject))
	}
	return claims{act: token.Act, scopes: token.Scopes}, nil
}

func scopeOf(procedure string, message any) (string, bool) {
	switch procedure {
	case variablestorev1connect.VariableStoreServiceSetValueProcedure,
		variablestorev1connect.VariableStoreServiceDeleteValueProcedure,
		variablestorev1connect.VariableStoreServiceSetReferenceProcedure,
		variablestorev1connect.VariableStoreServiceSetBindingProcedure,
		variablestorev1connect.VariableStoreServiceRemoveBindingProcedure,
		variablestorev1connect.VariableStoreServiceSyncEnvSourceProcedure,
		variablestorev1connect.VariableStoreServiceSetEnvSourceValueProcedure:
		return CapabilityVariablesWrite, true
	case variablestorev1connect.VariableStoreServiceRevealValuesProcedure:
		return CapabilityVariablesReveal, true
	case variablestorev1connect.VariableStoreServiceGetValueProcedure:
		if asked, ok := message.(*variablestorev1.GetValueRequest); ok && asked.GetReveal() {
			return CapabilityVariablesReveal, true
		}
		return CapabilityVariablesRead, true
	case variablestorev1connect.VariableStoreServiceListValuesProcedure,
		variablestorev1connect.VariableStoreServiceListReferencesProcedure,
		variablestorev1connect.VariableStoreServiceListVersionsProcedure,
		variablestorev1connect.VariableStoreServiceListBindingsProcedure,
		variablestorev1connect.VariableStoreServiceDescribeEnvSourceProcedure:
		return CapabilityVariablesRead, true
	}
	return "", false
}
