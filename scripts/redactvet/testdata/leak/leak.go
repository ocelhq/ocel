package leak

import (
	"fmt"
	"log"
	"log/slog"
	"testing"

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

type held struct {
	req *contractv1.DeployRequest
}

func wrapped(h held) error {
	return fmt.Errorf("deploy %v", h)
}

type heldByValue struct {
	registry contractv1.ImageRegistry
}

func reflected(h heldByValue) string {
	return fmt.Sprint(h) // want `heldByValue renders`
}

type heldInSlice struct {
	registries []contractv1.ImageRegistry
}

func reflectedElements(h *heldInSlice) string {
	return fmt.Sprintf("%+v", h) // want `\*heldInSlice renders`
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

func clean(runtime *contractv1.Runtime, tier fmt.Stringer, n int) string {
	return fmt.Sprintf("%v %s %d %s", runtime, tier, n, runtime.String())
}

func generic(m proto.Message, r protoreflect.ProtoMessage) string {
	return fmt.Sprint(m, r) // want `proto.Message renders` `protoreflect.ProtoMessage renders`
}

func cloned(req *contractv1.DeployRequest) string {
	return prototext.Format(proto.Clone(req)) // want `proto.Message renders`
}

type heldGeneric struct {
	Exposed proto.Message
	sealed  proto.Message
}

func genericField(h heldGeneric) string {
	return fmt.Sprint(h) // want `heldGeneric renders`
}

type sealedGeneric struct {
	m proto.Message
}

func genericSealed(s sealedGeneric) string {
	return fmt.Sprint(s)
}
