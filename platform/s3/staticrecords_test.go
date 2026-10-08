package s3

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func TestStaticRecordsListEachBindingUnderItsEnvNameAndHandBackItsRecord(t *testing.T) {
	uploads := &bindingsv1.Binding{Name: "uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-uploads"}}}
	events := &bindingsv1.Binding{Name: "events", Properties: &bindingsv1.Binding_Topic{Topic: &bindingsv1.TopicProperties{}}}

	records, err := NewStaticRecords(uploads, events)
	if err != nil {
		t.Fatalf("NewStaticRecords: %v", err)
	}

	bound := records.Bindings()
	if len(bound) != 2 || bound[0].Key != "OCEL_RESOURCE_BUCKET_uploads" || bound[0].Type != bindingsv1.BindingType_BINDING_TYPE_BUCKET ||
		bound[1].Key != "OCEL_RESOURCE_TOPIC_events" || bound[1].Type != bindingsv1.BindingType_BINDING_TYPE_TOPIC {
		t.Fatalf("Bindings() = %+v, want the bucket and the topic under the names the app reads them by", bound)
	}
	var read bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(records.Value("OCEL_RESOURCE_BUCKET_uploads")), &read); err != nil || read.GetBucket().GetBucket() != "shop-uploads" {
		t.Errorf("Value(uploads) = %q (%v), want the binding record", records.Value("OCEL_RESOURCE_BUCKET_uploads"), err)
	}
	if records.Value("OCEL_RESOURCE_BUCKET_missing") != "" {
		t.Error("Value of a binding nobody listed is not empty")
	}
}

func TestStaticRecordsRefuseABindingWithNoProperties(t *testing.T) {
	if _, err := NewStaticRecords(&bindingsv1.Binding{Name: "empty"}); err == nil {
		t.Error("NewStaticRecords of a binding with no properties = nil, want it refused: no type to list it under")
	}
}
