package bootstrap

import (
	"testing"

	"gopkg.in/yaml.v3"
)

type edgeUserTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			UserName string `yaml:"UserName"`
			Policies []struct {
				PolicyName     string `yaml:"PolicyName"`
				PolicyDocument struct {
					Statement []struct {
						Effect    string         `yaml:"Effect"`
						Action    any            `yaml:"Action"`
						Resource  any            `yaml:"Resource"`
						Condition map[string]any `yaml:"Condition"`
					} `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func TestEdgeUser(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		userName string
	}{
		{"production", featureTemplate(FeatureCloudflareEdge, ClassProduction), EdgeUserName},
		{"preview", featureTemplate(FeatureCloudflareEdge, ClassPreview), EdgePreviewUserName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tmpl edgeUserTemplate
			if err := yaml.Unmarshal([]byte(tc.template), &tmpl); err != nil {
				t.Fatalf("template is not valid YAML: %v", err)
			}
			user, ok := tmpl.Resources["EdgeUser"]
			if !ok {
				t.Fatal("template is missing the EdgeUser resource")
			}
			if user.Type != "AWS::IAM::User" {
				t.Errorf("EdgeUser Type = %q, want AWS::IAM::User", user.Type)
			}
			if user.Properties.UserName != tc.userName {
				t.Errorf("UserName = %q, want %q", user.Properties.UserName, tc.userName)
			}
			if len(user.Properties.Policies) != 1 {
				t.Fatalf("want exactly one inline policy, got %d", len(user.Properties.Policies))
			}

			if name := user.Properties.Policies[0].PolicyName; name != "ocel-edge-cache" {
				t.Errorf("PolicyName = %q, want ocel-edge-cache", name)
			}

			stmts := user.Properties.Policies[0].PolicyDocument.Statement
			var s3Read, s3Write, ddbTable, ddbIndex, sqsSend, invoke, invokeTagged bool
			for _, st := range stmts {
				if st.Resource == "${"+paramAssetBucketARN+"}/*" {
					s3Read = hasAction(st.Action, "s3:GetObject")
					if hasAction(st.Action, "s3:PutObject") {
						t.Error("s3:PutObject must not be granted bucket-wide")
					}
				}
				if st.Resource == "${"+paramAssetBucketARN+"}/*/fetch-cache/*.cache.json" {
					s3Write = hasAction(st.Action, "s3:PutObject")
				}
				if st.Resource == paramStateTableARN && boundToTagKeys(st.Condition) {
					ddbTable = hasAction(st.Action, "dynamodb:BatchGetItem") && hasAction(st.Action, "dynamodb:UpdateItem")
				}
				if st.Resource == "${"+paramStateTableARN+"}/index/"+StateTableIndexName && boundToTagKeys(st.Condition) {
					ddbIndex = hasAction(st.Action, "dynamodb:Query")
				}
				if st.Resource == paramRevalidateQueueARN {
					sqsSend = hasAction(st.Action, "sqs:SendMessage")
				}
				if hasAction(st.Action, "lambda:InvokeFunctionUrl") {
					invoke = true
					if equals, ok := st.Condition["StringEquals"].(map[string]any); ok {
						if equals["aws:ResourceTag/ocel:component"] == "function" {
							invokeTagged = true
						}
					}
				}
			}
			if !s3Read {
				t.Error("missing s3:GetObject on the asset bucket")
			}
			if !s3Write {
				t.Error("missing s3:PutObject scoped to a .cache.json object under the fetch-cache prefix")
			}
			if !ddbTable {
				t.Error("missing dynamodb:BatchGetItem + UpdateItem bounded to the TAG# LeadingKeys")
			}
			if !ddbIndex {
				t.Error("missing dynamodb:Query on the table's index bounded to the TAG# LeadingKeys")
			}
			if !sqsSend {
				t.Error("missing sqs:SendMessage on the revalidation queue the isr feature stood up")
			}
			if !invoke {
				t.Error("missing the lambda:Invoke* grant")
			}
			if !invokeTagged {
				t.Error("lambda:Invoke* grant must be gated on ocel:component being function, so it reaches no listener or other Ocel-run function")
			}
		})
	}
}
