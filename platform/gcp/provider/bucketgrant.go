package gcp

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
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

func bucketGrants(spec provider.StackSpec, account, member string) []grants.Grant {
	buckets := grantedBuckets(spec.App)
	if len(buckets) == 0 {
		return nil
	}
	granted := make([]grants.Grant, 0, len(buckets)+1)
	for _, bucket := range buckets {
		granted = append(granted, grants.On(grants.Bucket, bucket, bucketObjectsRole, member))
	}
	return append(granted, grants.On(grants.Account, account, tokenCreatorRole, member))
}
