package clitest

import (
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

const scopedTokenSeparator = "|"

type fakeBearer struct {
	token  string
	claims []string
	scoped bool
}

func scopedToken(token string, scopes []scopev1.Scope) string {
	return token + scopedTokenSeparator + strings.Join(scope.ClaimsOf(scopes), ",")
}

func readBearer(authorization string) (fakeBearer, bool) {
	raw, bearer := strings.CutPrefix(authorization, "Bearer ")
	if !bearer {
		return fakeBearer{}, false
	}
	token, claims, scoped := strings.Cut(raw, scopedTokenSeparator)
	read := fakeBearer{token: token, scoped: scoped}
	if claims != "" {
		read.claims = strings.Split(claims, ",")
	}
	return read, true
}

func (b fakeBearer) refuseUnheld(procedure string) error {
	if !b.scoped {
		return nil
	}
	required, err := scope.RequiredScope(procedure)
	if err != nil {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	claim := scope.ClaimsOf([]scopev1.Scope{required})
	if len(claim) == 1 && slices.Contains(b.claims, claim[0]) {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s needs the scope %v, and the token holds %v", procedure, required, b.claims))
}
