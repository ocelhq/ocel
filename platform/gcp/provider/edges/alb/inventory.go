package alb

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	costVendor      = "gcp"
	previewBaseItem = "preview"
)

var _ costkit.EdgeInventorier = (*Edge)(nil)

func (e *Edge) CostInventory(site costkit.EdgeSite) (costkit.EdgeInventory, error) {
	previewBase := ""
	if site.Class == edge.ClassPreview {
		previewBase = previewBaseItem
	}
	inventory := costkit.EdgeInventory{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Shared:      InventoryFront(site.Class, previewBase),
		Apps:        make(map[string][]costkit.Item, len(site.Apps)),
	}
	for _, app := range site.Apps {
		if hosts := InventoryHosts(site.Slug, site.Class, app.Hostnames); len(hosts) > 0 {
			inventory.Apps[app.Name] = hosts
		}
	}
	return inventory, nil
}

const (
	tfGlobalAddress        = "google_compute_global_address"
	tfBackendService       = "google_compute_backend_service"
	tfCertificateMap       = "google_certificate_manager_certificate_map"
	tfCertificateMapEntry  = "google_certificate_manager_certificate_map_entry"
	tfURLMap               = "google_compute_url_map"
	tfTargetHTTPSProxy     = "google_compute_target_https_proxy"
	tfGlobalForwardingRule = "google_compute_global_forwarding_rule"
	tfNetworkEndpointGroup = "google_compute_region_network_endpoint_group"
)

func InventoryFront(class edge.Class, previewBaseDomain string) []costkit.Item {
	held := frontNames(class)
	items := []costkit.Item{
		{Name: held.Address, Type: tfGlobalAddress, Properties: map[string]any{"address_type": "EXTERNAL"}},
		{Name: held.NotFound, Type: tfBackendService, Properties: backendProperties(false)},
		{Name: held.CertificateMap, Type: tfCertificateMap, Properties: map[string]any{}},
		{Name: held.URLMap, Type: tfURLMap, Properties: map[string]any{}},
		{Name: held.Proxy, Type: tfTargetHTTPSProxy, Properties: map[string]any{}},
		{Name: held.Rule, Type: tfGlobalForwardingRule, Properties: map[string]any{
			"load_balancing_scheme": externalManaged,
			"network_tier":          premiumTier,
			"port_range":            httpsPortRange,
		}},
	}
	if previewBaseDomain != "" {
		items = append(items,
			costkit.Item{Name: previewEntryName(previewBaseDomain), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			costkit.Item{Name: previewNEGName(previewBaseDomain), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			costkit.Item{Name: previewBackendName(previewBaseDomain), Type: tfBackendService, Properties: backendProperties(true)},
		)
	}
	return items
}

func InventoryHosts(slug string, class edge.Class, hostnames []string) []costkit.Item {
	var items []costkit.Item
	for _, hostname := range hostnames {
		items = append(items,
			costkit.Item{Name: entryName(slug, class, hostname), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			costkit.Item{Name: negName(slug, class, hostname), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			costkit.Item{Name: backendName(slug, class, hostname), Type: tfBackendService, Properties: backendProperties(true)},
		)
	}
	return items
}

func backendProperties(cdn bool) map[string]any {
	return map[string]any{
		"load_balancing_scheme": externalManaged,
		"protocol":              "HTTPS",
		"enable_cdn":            cdn,
	}
}
