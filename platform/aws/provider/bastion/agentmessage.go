package bastion

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	agentMessageHeaderBytes  = 116
	messageTypeBytes         = 32
	inputStreamData          = "input_stream_data"
	outputStreamData         = "output_stream_data"
	acknowledgeMessage       = "acknowledge"
	channelClosedMessage     = "channel_closed"
	flagData                 = 0
	flagAck                  = 3
	payloadUndefined         = 0
	payloadOutput            = 1
	payloadHandshakeRequest  = 5
	payloadHandshakeResponse = 6
	payloadHandshakeComplete = 7
	payloadFlag              = 10
	terminateSessionFlag     = 2
)

type agentMessage struct {
	messageType    string
	sequenceNumber int64
	flags          uint64
	id             uuid.UUID
	payloadType    uint32
	payload        []byte
}

func (m agentMessage) MarshalBinary() []byte {
	var buf bytes.Buffer
	buf.Grow(agentMessageHeaderBytes + 4 + len(m.payload))
	buf.Write(binary.BigEndian.AppendUint32(nil, agentMessageHeaderBytes))
	fmt.Fprintf(&buf, "%-*s", messageTypeBytes, m.messageType)
	buf.Write(binary.BigEndian.AppendUint32(nil, 1))
	buf.Write(binary.BigEndian.AppendUint64(nil, uint64(time.Now().UnixMilli())))
	buf.Write(binary.BigEndian.AppendUint64(nil, uint64(m.sequenceNumber)))
	buf.Write(binary.BigEndian.AppendUint64(nil, m.flags))
	buf.Write(m.id[8:])
	buf.Write(m.id[:8])
	digest := sha256.Sum256(m.payload)
	buf.Write(digest[:])
	buf.Write(binary.BigEndian.AppendUint32(nil, m.payloadType))
	buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(m.payload))))
	buf.Write(m.payload)
	return buf.Bytes()
}

func parseAgentMessage(data []byte) (agentMessage, error) {
	const payloadTypeOffset = 112
	if len(data) < payloadTypeOffset {
		return agentMessage{}, fmt.Errorf("the agent sent a %d-byte message, shorter than a message header", len(data))
	}
	headerBytes := binary.BigEndian.Uint32(data)
	if headerBytes < payloadTypeOffset || uint64(len(data)) < uint64(headerBytes)+4 {
		return agentMessage{}, fmt.Errorf("the agent sent a %d-byte message whose header claims %d bytes", len(data), headerBytes)
	}
	m := agentMessage{
		messageType:    strings.TrimRight(string(data[4:36]), " \x00"),
		sequenceNumber: int64(binary.BigEndian.Uint64(data[48:56])),
		flags:          binary.BigEndian.Uint64(data[56:64]),
	}
	copy(m.id[:8], data[72:80])
	copy(m.id[8:], data[64:72])
	if headerBytes >= payloadTypeOffset+4 {
		m.payloadType = binary.BigEndian.Uint32(data[payloadTypeOffset : payloadTypeOffset+4])
	}
	payloadBytes := binary.BigEndian.Uint32(data[headerBytes : headerBytes+4])
	payload := data[headerBytes+4:]
	if uint64(len(payload)) < uint64(payloadBytes) {
		return agentMessage{}, fmt.Errorf("the agent sent a %s message with %d of its %d payload bytes", m.messageType, len(payload), payloadBytes)
	}
	m.payload = bytes.Clone(payload[:payloadBytes])
	return m, nil
}
