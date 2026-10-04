// Command probe reads a buf image and a JSON input for probe.v1.Probe, parses it
// with protojson and writes protojson's canonical output, so the samples
// validated against each plugin's schema are what protojson emits.
package main

import (
	"fmt"
	"os"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: probe <image.binpb> <input.json>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	check(err)
	set := &descriptorpb.FileDescriptorSet{}
	check(proto.Unmarshal(raw, set))
	files, err := protodesc.NewFiles(set)
	check(err)
	types := new(protoregistry.Types)
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Messages().Len() {
			check(types.RegisterMessage(dynamicpb.NewMessageType(fd.Messages().Get(i))))
		}
		return true
	})
	desc, err := files.FindDescriptorByName("probe.v1.Probe")
	check(err)
	msg := dynamicpb.NewMessage(desc.(protoreflect.MessageDescriptor))
	input, err := os.ReadFile(os.Args[2])
	check(err)
	check(protojson.UnmarshalOptions{Resolver: types}.Unmarshal(input, msg))
	out, err := protojson.MarshalOptions{Resolver: types}.Marshal(msg)
	check(err)
	fmt.Println(string(out))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
