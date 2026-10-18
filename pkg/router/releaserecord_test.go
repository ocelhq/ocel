package router

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/typescript"
)

func TestTheContractsReleaseRecordIsTheOneTheGoRouterDeclares(t *testing.T) {
	typescript.RequireDeclared(t,
		filepath.Join("..", "..", "platform", "edge", "contract", "src", "release-record.mts"),
		typescript.Header("go test ./pkg/router -run TestTheContractsReleaseRecordIsTheOneTheGoRouterDeclares -update"),
		[]typescript.Import{{Type: reflect.TypeFor[edge.Static](), Name: "Static", From: "./hosting.mjs"}},
		reflect.TypeFor[ReleaseRecord](),
	)
}
