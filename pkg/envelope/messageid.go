package envelope

import (
	"crypto/rand"
	"crypto/sha256"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func NewMessageID(at time.Time) string {
	var entropy [10]byte
	_, _ = rand.Read(entropy[:])
	return encodeMessageID(at, entropy)
}

func MessageIDFrom(at time.Time, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	var entropy [10]byte
	copy(entropy[:], sum[:])
	return encodeMessageID(at, entropy)
}

func encodeMessageID(at time.Time, entropy [10]byte) string {
	var id [16]byte
	ms := uint64(at.UnixMilli())
	for i := range 6 {
		id[i] = byte(ms >> (40 - 8*i))
	}
	copy(id[6:], entropy[:])
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
