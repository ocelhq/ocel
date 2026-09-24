package drift // want package:`carriers\(Secret\)`

const file_drift_proto_rawDesc = "\n\vdrift.proto\x12\x05drift\"\x1c\n\x06Secret\x12\x12\n\x05value\x18\x01 \x01(\tB\x03\x80\x01\x01b\x06proto3" // want `no Go type Secret for .drift.Secret`

type Sealed struct {
	Value string
}
