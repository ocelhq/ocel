package gcp

func newCDNPurgeRole(names Names) customRole {
	return customRole{
		id:          names.CDNPurgeRole(),
		title:       "ocel cache purge (" + string(names.namespace) + ")",
		description: "lets a Next app behind the alb clear its revalidated pages from Cloud CDN, and nothing else",
		permissions: []string{"compute.urlMaps.invalidateCache"},
		driftReason: "it holds other permissions than clearing Cloud CDN, which is all a Next app behind the alb is granted it for",
	}
}
