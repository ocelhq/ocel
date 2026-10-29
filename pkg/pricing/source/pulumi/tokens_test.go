package pulumi

import "testing"

func TestATypeTokenMapsToItsTerraformType(t *testing.T) {
	for _, c := range []struct{ token, vendor, typ string }{
		{"aws:lambda/function:Function", "aws", "aws_lambda_function"},
		{"aws:rds/clusterInstance:ClusterInstance", "aws", "aws_rds_cluster_instance"},
		{"aws:cloudwatch/logGroup:LogGroup", "aws", "aws_cloudwatch_log_group"},
		{"aws:s3/bucketV2:BucketV2", "aws", "aws_s3_bucket"},
		{"aws:lb/loadBalancer:LoadBalancer", "aws", "aws_lb"},
		{"aws:rds/instance:Instance", "aws", "aws_db_instance"},
		{"aws:ec2/natGateway:NatGateway", "aws", "aws_nat_gateway"},
		{"gcp:cloudrunv2/service:Service", "gcp", "google_cloud_run_v2_service"},
		{"gcp:compute/uRLMap:URLMap", "gcp", "google_compute_url_map"},
		{"gcp:projects/iAMMember:IAMMember", "gcp", "google_project_iam_member"},
		{"gcp:storage/bucket:Bucket", "gcp", "google_storage_bucket"},
		{"cloudflare:index/workersScript:WorkersScript", "cloudflare", "cloudflare_workers_script"},
		{"aws-native:s3:Bucket", "aws-native", "aws-native_s3_bucket"},
	} {
		vendor, typ, ok := terraformType(c.token)
		if !ok || vendor != c.vendor || typ != c.typ {
			t.Errorf("%s = (%q, %q, %v), want (%q, %q)", c.token, vendor, typ, ok, c.vendor, c.typ)
		}
	}
}

func TestATokenWithoutThreePartsHasNoTerraformType(t *testing.T) {
	if _, _, ok := terraformType("aws:lambda"); ok {
		t.Error("a two-part token mapped")
	}
}
