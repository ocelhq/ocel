package provider

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
)

type PhysicalResource struct {
	Kind       string
	ID         string
	Location   string `json:",omitempty"`
	ConsoleURL string `json:",omitempty"`
}

type ProjectResourcesRequest struct {
	Tier           environment.Tier
	Project        string
	EdgeKind       edge.Kind
	EdgeStack      edge.StackState
	Routers        map[router.Kind]router.StackState
	DNSKind        DNSKind
	WrittenRecords []edge.Record
	Certificates   []Certificate
	Images         []string
}
