package providerserver

import (
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
)

func OwnerOf(key records.Key) (records.Owner, bool) {
	for _, classify := range []func(records.Key) (records.Owner, bool){ownerOfDeployedSettings, ownerOfInvalidationTargets, ownerOfHostRecord} {
		if owner, known := classify(key); known {
			return owner, true
		}
	}
	return records.Owner{}, false
}

func OwnerOfAssociatedData(bound seal.AssociatedData) (records.Owner, bool) {
	for _, classify := range []func(seal.AssociatedData) (records.Owner, bool){ownerOfReleaseSecrets, ownerOfPreviewKey, ownerOfHostAssociatedData} {
		if owner, known := classify(bound); known {
			return owner, true
		}
	}
	return records.Owner{}, false
}
