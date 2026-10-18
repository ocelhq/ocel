package cfn

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func sampleTemplate() Template {
	return Template{
		Description: "a <sample> & its bucket",
		Parameters:  []Parameter{{Name: "Upstream", Type: "String", Description: "ARN handed down"}},
		Resources: []Resource{
			{ID: "Zeta", Type: "AWS::S3::Bucket", Metadata: Metadata{Description: "first declared"}, Properties: map[string]any{
				"BucketName": Sub("${AWS::StackName}-zeta"),
			}},
			{ID: "Alpha", Type: "AWS::CloudFormation::WaitConditionHandle"},
		},
		Outputs: []Output{
			{Name: "ZetaArn", Description: "its ARN", Value: GetAtt("Zeta", "Arn")},
			{Name: "ZetaName", Description: "its name", Value: Ref("Zeta")},
		},
	}
}

func TestABodyListsResourcesAndOutputsInTheOrderTheTemplateDeclaresThem(t *testing.T) {
	t.Parallel()

	body, err := sampleTemplate().Body()
	if err != nil {
		t.Fatal(err)
	}
	for _, earlier := range [][2]string{
		{`"AWSTemplateFormatVersion"`, `"Description"`},
		{`"Description"`, `"Parameters"`},
		{`"Parameters"`, `"Resources"`},
		{`"Resources"`, `"Outputs"`},
		{`"Zeta"`, `"Alpha"`},
		{`"ZetaArn"`, `"ZetaName"`},
		{`"Type"`, `"Metadata"`},
		{`"Metadata"`, `"Properties"`},
	} {
		if strings.Index(body, earlier[0]) > strings.Index(body, earlier[1]) {
			t.Errorf("%s comes after %s in\n%s", earlier[0], earlier[1], body)
		}
	}
}

func TestABodyIsTheSameEveryTimeItIsRendered(t *testing.T) {
	t.Parallel()

	first, err := sampleTemplate().Body()
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := sampleTemplate().Body()
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("a second render differs:\n%s\n---\n%s", first, again)
		}
	}
}

func TestABodyWritesIntrinsicsAsTheFunctionsCloudFormationReads(t *testing.T) {
	t.Parallel()

	body, err := sampleTemplate().Body()
	if err != nil {
		t.Fatal(err)
	}
	var read map[string]any
	if err := json.Unmarshal([]byte(body), &read); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	if read["AWSTemplateFormatVersion"] != FormatVersion || read["Description"] != "a <sample> & its bucket" {
		t.Errorf("header = %v, %v", read["AWSTemplateFormatVersion"], read["Description"])
	}
	resources := read["Resources"].(map[string]any)
	zeta := resources["Zeta"].(map[string]any)
	if got := zeta["Properties"].(map[string]any)["BucketName"]; !reflect.DeepEqual(got, map[string]any{"Fn::Sub": "${AWS::StackName}-zeta"}) {
		t.Errorf("BucketName = %v, want an Fn::Sub", got)
	}
	if got := zeta["Metadata"]; !reflect.DeepEqual(got, map[string]any{"Description": "first declared"}) {
		t.Errorf("Zeta Metadata = %v", got)
	}
	alpha := resources["Alpha"].(map[string]any)
	if _, ok := alpha["Metadata"]; ok {
		t.Errorf("Alpha has Metadata %v, and it declares none", alpha["Metadata"])
	}
	if _, ok := alpha["Properties"]; ok {
		t.Errorf("Alpha has Properties %v, and it declares none", alpha["Properties"])
	}
	outputs := read["Outputs"].(map[string]any)
	if got := outputs["ZetaArn"].(map[string]any)["Value"]; !reflect.DeepEqual(got, map[string]any{"Fn::GetAtt": []any{"Zeta", "Arn"}}) {
		t.Errorf("ZetaArn = %v, want an Fn::GetAtt", got)
	}
	if got := outputs["ZetaName"].(map[string]any)["Value"]; !reflect.DeepEqual(got, map[string]any{"Ref": "Zeta"}) {
		t.Errorf("ZetaName = %v, want a Ref", got)
	}
	parameter := read["Parameters"].(map[string]any)["Upstream"]
	if !reflect.DeepEqual(parameter, map[string]any{"Type": "String", "Description": "ARN handed down"}) {
		t.Errorf("Upstream = %v", parameter)
	}
}

func TestABodyWithNoParametersOrOutputsDeclaresNeitherSection(t *testing.T) {
	t.Parallel()

	body, err := Template{Description: "bare", Resources: []Resource{{ID: "Handle", Type: "AWS::CloudFormation::WaitConditionHandle"}}}.Body()
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{`"Parameters"`, `"Outputs"`} {
		if strings.Contains(body, section) {
			t.Errorf("the body declares %s:\n%s", section, body)
		}
	}
}
