package pulumi

import (
	"strings"
	"unicode"
)

type vendorPackage struct {
	vendor string
	prefix string
}

var packages = map[string]vendorPackage{
	"aws":        {vendor: "aws", prefix: "aws_"},
	"gcp":        {vendor: "gcp", prefix: "google_"},
	"cloudflare": {vendor: "cloudflare", prefix: "cloudflare_"},
}

var bridged = map[string]string{
	"aws:apigateway/restApi":                     "aws_api_gateway_rest_api",
	"aws:ec2/eip":                                "aws_eip",
	"aws:ec2/instance":                           "aws_instance",
	"aws:ec2/internetGateway":                    "aws_internet_gateway",
	"aws:ec2/natGateway":                         "aws_nat_gateway",
	"aws:ec2/route":                              "aws_route",
	"aws:ec2/routeTable":                         "aws_route_table",
	"aws:ec2/routeTableAssociation":              "aws_route_table_association",
	"aws:ec2/securityGroup":                      "aws_security_group",
	"aws:ec2/subnet":                             "aws_subnet",
	"aws:ec2/vpc":                                "aws_vpc",
	"aws:ec2/vpcEndpoint":                        "aws_vpc_endpoint",
	"aws:elasticache/cluster":                    "aws_elasticache_cluster",
	"aws:lb/listener":                            "aws_lb_listener",
	"aws:lb/loadBalancer":                        "aws_lb",
	"aws:lb/targetGroup":                         "aws_lb_target_group",
	"aws:rds/instance":                           "aws_db_instance",
	"gcp:artifactregistry/repository":            "google_artifact_registry_repository",
	"gcp:certificatemanager/certificateMap":      "google_certificate_manager_certificate_map",
	"gcp:certificatemanager/certificateMapEntry": "google_certificate_manager_certificate_map_entry",
	"gcp:cloudrunv2/service":                     "google_cloud_run_v2_service",
	"gcp:compute/uRLMap":                         "google_compute_url_map",
	"gcp:secretmanager/secret":                   "google_secret_manager_secret",
	"gcp:serviceaccount/account":                 "google_service_account",
}

func token(typ string) (vendor, tf string, ok bool) {
	pkg, rest, found := strings.Cut(typ, ":")
	if !found {
		return "", "", false
	}
	module, name, found := strings.Cut(rest, ":")
	if !found {
		return "", "", false
	}
	held, known := packages[pkg]
	if !known {
		held = vendorPackage{vendor: pkg, prefix: pkg + "_"}
	}
	if mapped, exceptional := bridged[pkg+":"+module]; exceptional {
		return held.vendor, mapped, true
	}
	group, _, _ := strings.Cut(module, "/")
	if group == "index" {
		group = ""
	}
	if group != "" {
		group = snake(group) + "_"
	}
	local := group + snake(name)
	if strings.HasPrefix(local, held.prefix) {
		return held.vendor, local, true
	}
	return held.vendor, held.prefix + local, true
}

func snake(s string) string {
	runes := []rune(s)
	var out []rune
	for i, r := range runes {
		if !unicode.IsUpper(r) {
			out = append(out, r)
			continue
		}
		leadsAWord := i > 0 && (!unicode.IsUpper(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1])))
		if leadsAWord {
			out = append(out, '_')
		}
		out = append(out, unicode.ToLower(r))
	}
	return string(out)
}
