package cloudflare

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/typescript"
)

func TestTheEntryWorkersServedRecordIsTheOneThisRouterDelivers(t *testing.T) {
	typescript.RequireDeclared(t,
		filepath.Join("..", "workers", "entry", "src", "served-record.ts"),
		typescript.Header("go test ./platform/edge/cloudflare/deploy -run TestTheEntryWorkersServedRecordIsTheOneThisRouterDelivers -update"),
		[]typescript.Import{{Type: reflect.TypeFor[router.ReleaseRecord](), Name: "ReleaseRecord", From: "@platform/edge-contract/release-record"}},
		reflect.TypeFor[servedRecord](),
	)
}
