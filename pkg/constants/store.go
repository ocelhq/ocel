package constants

const objectStoreImage = "rustfs/rustfs:1.0.0@sha256:8cc9801755448b71a786705ce76692c77e14936cccd87cf2fc31842e58f4d1ff"

// ObjectStoreImage is the S3-compatible store a box runs for the buckets a
// project declares, pinned to the multi-arch index that serves amd64 and arm64.
func ObjectStoreImage() string { return objectStoreImage }
