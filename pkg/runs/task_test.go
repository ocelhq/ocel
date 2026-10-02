package runs_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestATaskIsATopicWithOneExclusiveConsumer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		spec provider.TopicSpec
		want bool
	}{
		{"one exclusive consumer", provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: "resize", Exclusive: true}}}, true},
		{"one shared consumer", provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: "ship"}}}, false},
		{"two consumers", provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: "ship", Exclusive: true}, {Name: "bill"}}}, false},
		{"no consumer", provider.TopicSpec{}, false},
	} {
		if got := runs.IsTaskSpec(&tc.spec); got != tc.want {
			t.Errorf("%s: IsTaskSpec() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
