package pgmq

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newMessageID(at time.Time) string {
	var id [16]byte
	ms := uint64(at.UnixMilli())
	for i := range 6 {
		id[i] = byte(ms >> (40 - 8*i))
	}
	_, _ = rand.Read(id[6:])
	var text [26]byte
	text[0] = crockford[id[0]>>5]
	bits, held, written := uint32(id[0]&0x1f), 5, 1
	for _, b := range id[1:] {
		bits = bits<<8 | uint32(b)
		held += 8
		for held >= 5 {
			held -= 5
			text[written] = crockford[(bits>>held)&0x1f]
			written++
		}
	}
	return string(text[:])
}
