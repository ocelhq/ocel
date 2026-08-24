package providerkit

import (
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type RecordStore = ports.RecordStore

type RecordName = ports.RecordName

type Revision = ports.Revision

type Record = ports.Record

var (
	ErrStale    = ports.ErrStale
	ErrNoRecord = ports.ErrNoRecord
)

var (
	Held   = ports.Held
	Forget = ports.Forget
)

const (
	RootSchema      = ports.RootSchema
	RootProjects    = ports.RootProjects
	RootBootstrap   = ports.RootBootstrap
	RootEdgeStacks  = ports.RootEdgeStacks
	RootWildcard    = ports.RootWildcard
	RootLedger      = ports.RootLedger
	RootValues      = ports.RootValues
	RootValueRefs   = ports.RootValueRefs
	RootConformance = ports.RootConformance
)
