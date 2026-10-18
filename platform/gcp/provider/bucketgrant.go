package gcp

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
)

const bucketObjectsRole = "roles/storage.objectAdmin"

func grantedBuckets(app *provider.AppSpec) []string {
	var granted []string
	for _, binding := range slices.Concat(app.Values.Bindings, app.Grants) {
		name := binding.Properties[provider.PropertyBucket]
		if binding.Type == provider.BindingBucket && !binding.Endpointed() && name != "" && !slices.Contains(granted, name) {
			granted = append(granted, name)
		}
	}
	return granted
}

func bucketGrants(spec provider.StackSpec, account, member string) []appGrant {
	buckets := grantedBuckets(spec.App)
	if len(buckets) == 0 {
		return nil
	}
	grants := make([]appGrant, 0, len(buckets)+1)
	for _, bucket := range buckets {
		grants = append(grants, on(targetBucket, bucket, bucketObjectsRole, member))
	}
	return append(grants, on(targetAccount, account, tokenCreatorRole, member))
}
