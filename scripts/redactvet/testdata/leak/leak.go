package leak

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func printed(req *contractv1.DeployRequest) string {
	return fmt.Sprintf("%v", req) // want `contractv1.DeployRequest renders its debug_redact fields in clear`
}

func stringed(registry *contractv1.ImageRegistry) string {
	return registry.String() // want `contractv1.ImageRegistry renders its debug_redact fields in clear`
}

func nested(manifest *contractv1.Manifest, apps []*contractv1.ManifestApp) {
	log.Printf("%+v", manifest) // want `contractv1.Manifest renders`
	log.Println(apps)           // want `\[\]\*contractv1.ManifestApp renders`
}

func logged(logger *slog.Logger, value *envvarsv1.RevealedValue, postgres *bindingsv1.PostgresProperties) {
	logger.Info("revealed", "value", value)      // want `envvarsv1.RevealedValue renders`
	slog.Info("bound", slog.Any("pg", postgres)) // want `bindingsv1.PostgresProperties renders`
}

func asserted(t *testing.T, got *contractv1.ResolveImageRegistryResponse) {
	t.Errorf("resolved %v", got) // want `contractv1.ResolveImageRegistryResponse renders`
}

func text(req *contractv1.DeployRequest) string {
	return prototext.Format(req) // want `contractv1.DeployRequest renders`
}

type requestPointer struct {
	req *contractv1.DeployRequest
}

func wrapped(h requestPointer) error {
	return fmt.Errorf("deploy %v", h)
}

type registryValue struct {
	registry contractv1.ImageRegistry
}

func reflected(h registryValue) string {
	return fmt.Sprint(h) // want `registryValue renders`
}

type registrySlice struct {
	registries []contractv1.ImageRegistry
}

func reflectedElements(h *registrySlice) string {
	return fmt.Sprintf("%+v", h) // want `\*registrySlice renders`
}

type exposed struct {
	Req *contractv1.DeployRequest
}

func exported(e exposed) string {
	return fmt.Sprint(e) // want `exposed renders`
}

type sealed struct {
	inner exposed
}

func sealedAway(s sealed) string {
	return fmt.Sprint(s)
}

type reopened struct {
	exposed
}

func embeddedUnexported(r reopened) string {
	return fmt.Sprint(r) // want `reopened renders`
}

type indirect struct {
	Inner *exposed
}

func addressOnly(i indirect) string {
	return fmt.Sprint(i)
}

type pointerDescribed struct {
	Req *contractv1.DeployRequest
}

func (d *pointerDescribed) String() string { return d.Req.GetTag() }

func describedByValue(d pointerDescribed, p *pointerDescribed) string {
	return fmt.Sprint(d, p) // want `pointerDescribed renders`
}

type embedding struct {
	*contractv1.ImageRegistry
}

func promoted(e embedding) string {
	return fmt.Sprint(e) // want `embedding renders`
}

type described struct {
	req *contractv1.DeployRequest
}

func (d described) String() string { return d.req.GetTag() }

func owned(d described) string {
	return fmt.Sprint(d)
}

func fields(req *contractv1.DeployRequest) string {
	return fmt.Sprintf("%s tagged %s", req.GetImageRegistry().GetServer(), req.GetTag())
}

func clean(framework *contractv1.Framework, tier fmt.Stringer, n int) string {
	return fmt.Sprintf("%v %s %d %s", framework, tier, n, framework.String())
}

func generic(m proto.Message, r protoreflect.ProtoMessage) string {
	return fmt.Sprint(m, r) // want `proto.Message renders` `protoreflect.ProtoMessage renders`
}

func cloned(req *contractv1.DeployRequest) string {
	return prototext.Format(proto.Clone(req)) // want `proto.Message renders`
}

type exposedGeneric struct {
	Exposed proto.Message
	sealed  proto.Message
}

func genericField(h exposedGeneric) string {
	return fmt.Sprint(h) // want `exposedGeneric renders`
}

type sealedGeneric struct {
	m proto.Message
}

func genericSealed(s sealedGeneric) string {
	return fmt.Sprint(s)
}

func marshalled(req *contractv1.DeployRequest) ([]byte, error) {
	return json.Marshal(req) // want `contractv1.DeployRequest encodes its debug_redact fields`
}

func indented(registry contractv1.ImageRegistry) ([]byte, error) {
	return json.MarshalIndent(registry, "", "  ") // want `contractv1.ImageRegistry encodes`
}

func streamed(w io.Writer, values map[string]*envvarsv1.RevealedValue) error {
	return json.NewEncoder(w).Encode(values) // want `map\[string\]\*envvarsv1.RevealedValue encodes`
}

func followed(i indirect, h requestPointer, m proto.Message) ([]byte, error) {
	if _, err := json.Marshal(h); err != nil {
		return nil, err
	}
	if _, err := json.Marshal(m); err != nil { // want `proto.Message encodes`
		return nil, err
	}
	return json.Marshal(i) // want `indirect encodes`
}

type promotedJSON struct {
	sealedEmbedded
}

type sealedEmbedded struct {
	Req *contractv1.DeployRequest
}

type skipped struct {
	Req *contractv1.DeployRequest `json:"-"`
	req *contractv1.DeployRequest
}

type marshaler struct {
	Req *contractv1.DeployRequest
}

func (marshaler) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

func tagged(p promotedJSON, s skipped, d marshaler) ([]byte, error) {
	if _, err := json.Marshal(s); err != nil {
		return nil, err
	}
	if _, err := json.Marshal(d); err != nil {
		return nil, err
	}
	return json.Marshal(p) // want `promotedJSON encodes`
}

func decoded(data []byte, registry *contractv1.ImageRegistry) error {
	return json.Unmarshal(data, registry)
}

func wire(req *contractv1.DeployRequest) ([]byte, error) {
	return protojson.Marshal(req)
}

func slogged(logger *slog.Logger, i indirect) {
	logger.Info("deploy", "inner", i) // want `indirect encodes`
}

type pointerMarshaler struct {
	Req *contractv1.DeployRequest
}

func (*pointerMarshaler) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

func addressable(p pointerMarshaler, all []pointerMarshaler) ([]byte, error) {
	if _, err := json.Marshal(&p); err != nil {
		return nil, err
	}
	if _, err := json.Marshal(all); err != nil {
		return nil, err
	}
	return json.Marshal(p) // want `pointerMarshaler encodes`
}
