package ocel

// Thresholds forces the sizes the writer switches to a multipart upload at, so
// a test can drive that path without moving megabytes.
func (b *BucketStore) Thresholds(single, part int64) {
	b.singleCeiling = single
	b.partSize = part
}
