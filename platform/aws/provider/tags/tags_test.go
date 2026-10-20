package tags

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
)

func TestSortedHandsBackEveryTagInKeyOrderAsTheSDKsOwnType(t *testing.T) {
	got := Sorted(map[string]string{"ocel:managed-by": "ocel", "Name": "web", "ocel:env-tier": "production"},
		func(key, value *string) acmtypes.Tag { return acmtypes.Tag{Key: key, Value: value} })

	want := []acmtypes.Tag{
		{Key: aws.String("Name"), Value: aws.String("web")},
		{Key: aws.String("ocel:env-tier"), Value: aws.String("production")},
		{Key: aws.String("ocel:managed-by"), Value: aws.String("ocel")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sorted = %v, want %v", got, want)
	}
}
