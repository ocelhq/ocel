package pulumi

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

var renamed = map[string]string{
	"aws:alb/loadBalancer":             "aws_lb",
	"aws:ec2/instance":                 "aws_instance",
	"aws:ec2/launchTemplate":           "aws_launch_template",
	"aws:ec2/route":                    "aws_route",
	"aws:ec2/routeTable":               "aws_route_table",
	"aws:ec2/routeTableAssociation":    "aws_route_table_association",
	"aws:ec2/securityGroup":            "aws_security_group",
	"aws:elasticache/cluster":          "aws_elasticache_cluster",
	"aws:lb/listener":                  "aws_lb_listener",
	"aws:lb/targetGroup":               "aws_lb_target_group",
	"aws:rds/instance":                 "aws_db_instance",
	"aws:s3/bucketV2":                  "aws_s3_bucket",
	"gcp:cloudfunctionsv2/function":    "google_cloudfunctions2_function",
	"gcp:cloudrun/service":             "google_cloud_run_service",
	"gcp:cloudrunv2/job":               "google_cloud_run_v2_job",
	"gcp:servicenetworking/connection": "google_service_networking_connection",
	"gcp:vpcaccess/connector":          "google_vpc_access_connector",
}

func terraformType(token string) (vendor, typ string, ok bool) {
	pkg, rest, found := strings.Cut(token, ":")
	if !found {
		return "", "", false
	}
	module, name, found := strings.Cut(rest, ":")
	if !found || name == "" {
		return "", "", false
	}
	if typ, bridgedToken := bridgedTokens[token]; bridgedToken {
		return pkg, typ, true
	}
	if mapped, exceptional := renamed[pkg+":"+module]; exceptional {
		return pkg, mapped, true
	}
	group, _, _ := strings.Cut(module, "/")
	if group == "index" {
		group = ""
	}
	if group != "" {
		group = properties.Snake(group) + "_"
	}
	return pkg, properties.TypePrefix(pkg) + group + properties.Snake(name), true
}
