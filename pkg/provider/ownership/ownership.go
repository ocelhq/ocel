package ownership

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

var keyOwners = map[records.Root]func(records.Key) (records.Owner, bool){
	stackrecords.RootProjects:           stackrecords.OwnerOf,
	stackrecords.RootStacks:             stackrecords.OwnerOf,
	stackrecords.RootEnvironments:       stackrecords.OwnerOf,
	stackrecords.RootEnvironmentLeases:  stackrecords.OwnerOf,
	stackrecords.RootProjectLeases:      stackrecords.OwnerOf,
	stackrecords.RootBootstrap:          stackrecords.OwnerOf,
	stackrecords.RootEdgeStacks:         stackrecords.OwnerOf,
	stackrecords.RootWildcard:           stackrecords.OwnerOf,
	stackrecords.RootRouters:            providerserver.OwnerOf,
	ledger.Root:                         ledger.OwnerOf,
	variablestore.RootValues:            variablestore.OwnerOf,
	variablestore.RootValueRefs:         variablestore.OwnerOf,
	envsource.RootRegistrations:         envsource.OwnerOf,
	envsource.RootStatus:                envsource.OwnerOf,
	envsource.RootDigestKey:             envsource.OwnerOf,
	resources.RootSharedPhysicals:       resources.OwnerOf,
	providerserver.RootDeployedSettings: providerserver.OwnerOf,
	providerserver.RootSessions:         providerserver.OwnerOf,
	providerserver.RootQuestions:        providerserver.OwnerOf,
}

var associatedDataOwners = []func(seal.AssociatedData) (records.Owner, bool){
	variablestore.OwnerOfAssociatedData,
	providerserver.OwnerOfAssociatedData,
	envsource.OwnerOfAssociatedData,
	stackrecords.OwnerOfAssociatedData,
}

func OwnerOfKey(key records.Key) (records.Owner, bool) {
	classify, known := keyOwners[key.Partition.Root]
	if !known {
		return records.Owner{}, false
	}
	return classify(key)
}

func OwnerOfAssociatedData(bound seal.AssociatedData) (records.Owner, bool) {
	for _, classify := range associatedDataOwners {
		if owner, known := classify(bound); known {
			return owner, true
		}
	}
	return records.Owner{}, false
}

func ListRoots() []records.Root {
	return slices.Sorted(maps.Keys(keyOwners))
}
