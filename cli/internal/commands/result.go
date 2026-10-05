package commands

import (
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const resultsAnnotation = "ocel.results"

func DeclareResult(cmd *cobra.Command, result proto.Message, more ...proto.Message) *cobra.Command {
	names := make([]string, 0, 1+len(more))
	for _, each := range append([]proto.Message{result}, more...) {
		names = append(names, string(each.ProtoReflect().Descriptor().FullName()))
	}
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[resultsAnnotation] = strings.Join(names, ",")
	return cmd
}

func FindResults(cmd *cobra.Command) (results []protoreflect.FullName, declared bool) {
	value, declared := cmd.Annotations[resultsAnnotation]
	if !declared {
		return nil, false
	}
	for name := range strings.SplitSeq(value, ",") {
		results = append(results, protoreflect.FullName(name))
	}
	return results, true
}
