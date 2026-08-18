package edge

import (
	"slices"
	"time"
)

type declaration struct {
	needs      []Need
	flip       FlipBound
	originCert bool
}

const propagationBound = 5 * time.Second

var declared = map[Kind]declaration{
	KindCloudflare: {needs: AllNeeds(), flip: FlipBound{}},
	KindNative:     {needs: []Need{NeedEdgeCache, NeedStreaming}, flip: FlipBound{Typical: propagationBound}, originCert: true},
	KindNone:       {needs: []Need{NeedStreaming}, flip: FlipBound{Typical: propagationBound}, originCert: true},
}

type Capabilities struct {
	kind Kind
}

func CapabilitiesOf(kind Kind) Capabilities {
	return Capabilities{kind: kind}
}

func (c Capabilities) Supported() []Need {
	return slices.Clone(declared[c.kind].needs)
}

func (c Capabilities) Supports(need Need) bool {
	return slices.Contains(declared[c.kind].needs, need)
}

func (c Capabilities) FlipBound() FlipBound {
	return declared[c.kind].flip
}

func (c Capabilities) NeedsOriginCertificate() bool {
	return declared[c.kind].originCert
}
