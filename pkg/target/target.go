package target

import (
	"fmt"
	"strings"
)

type Vendor string

const (
	AWS Vendor = "aws"
	GCP Vendor = "gcp"
	VPS Vendor = "vps"
)

func Fingerprint(vendor Vendor, parts ...string) (string, error) {
	named := map[Vendor][]string{
		AWS: {"account id", "region", "namespace"},
		GCP: {"project", "region", "namespace"},
		VPS: {"host key fingerprint", "namespace"},
	}[vendor]
	if named == nil {
		return "", fmt.Errorf("no target shape is defined for the %q vendor", string(vendor))
	}
	if len(parts) != len(named) {
		return "", fmt.Errorf("a %s target takes %s, so %d parts, not %d",
			vendor, strings.Join(named, ", "), len(named), len(parts))
	}
	for at, part := range parts {
		if part == "" {
			return "", fmt.Errorf("a %s target needs a %s", vendor, named[at])
		}
		if strings.ContainsAny(part, "/ \t\n") {
			return "", fmt.Errorf("the %s of a %s target may not contain a slash or whitespace: %q", named[at], vendor, part)
		}
	}
	return string(vendor) + "/" + strings.Join(parts, "/"), nil
}

func ForAWS(accountID, region, namespace string) (string, error) {
	return Fingerprint(AWS, accountID, region, namespace)
}

func ForGCP(project, region, namespace string) (string, error) {
	return Fingerprint(GCP, project, region, namespace)
}

func ForVPS(hostKey, namespace string) (string, error) {
	return Fingerprint(VPS, hostKey, namespace)
}
