package bucket

// Thresholds forces the sizes the writer switches to a multipart upload at, so
// a test can drive that path without moving megabytes.
func (b *Bucket) Thresholds(single, part int64) {
	b.singleCeiling = single
	b.partSize = part
}
