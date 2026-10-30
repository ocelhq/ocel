package properties

import "strings"

const VendorGCP = "gcp"

var typePrefixes = map[string]string{
	"aws":        "aws_",
	VendorGCP:    "google_",
	"cloudflare": "cloudflare_",
}

func TypePrefix(vendor string) string {
	if prefix, known := typePrefixes[vendor]; known {
		return prefix
	}
	return vendor + "_"
}

func TypeVendor(typ string) string {
	for vendor, prefix := range typePrefixes {
		if strings.HasPrefix(typ, prefix) {
			return vendor
		}
	}
	vendor, _, _ := strings.Cut(typ, "_")
	return vendor
}
