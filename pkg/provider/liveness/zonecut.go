package liveness

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

const (
	dnsHeaderBytes    = 12
	dnsReplyBytes     = 512
	dnsTypeNS         = 2
	dnsClassIN        = 1
	dnsFlagResponse   = 0x8000
	dnsFlagAuthority  = 0x0400
	dnsRcodeMask      = 0x000f
	dnsRcodeNameError = 3
	dnsLabelBytes     = 63
	dnsNameBytes      = 255
)

type nameserverDNS struct {
	netDNS
	nameserver string
	dial       func(ctx context.Context, network, address string) (net.Conn, error)
}

func (d nameserverDNS) IsZoneCut(ctx context.Context, name string) (bool, error) {
	query, id, err := encodeNSQuery(name)
	if err != nil {
		return false, err
	}
	conn, err := d.dial(ctx, "udp", net.JoinHostPort(d.nameserver, "53"))
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(query); err != nil {
		return false, err
	}
	reply := make([]byte, dnsReplyBytes)
	n, err := conn.Read(reply)
	if err != nil {
		return false, err
	}
	return isReferral(reply[:n], id)
}

func encodeNSQuery(name string) ([]byte, uint16, error) {
	var header [dnsHeaderBytes]byte
	if _, err := rand.Read(header[:2]); err != nil {
		return nil, 0, err
	}
	binary.BigEndian.PutUint16(header[4:], 1)
	query := header[:]
	for label := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if label == "" || len(label) > dnsLabelBytes {
			return nil, 0, fmt.Errorf("%s is not a name a nameserver can be asked about", name)
		}
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0)
	if len(query)-dnsHeaderBytes > dnsNameBytes {
		return nil, 0, fmt.Errorf("%s is longer than a name a nameserver can be asked about", name)
	}
	query = binary.BigEndian.AppendUint16(query, dnsTypeNS)
	query = binary.BigEndian.AppendUint16(query, dnsClassIN)
	return query, binary.BigEndian.Uint16(header[:2]), nil
}

func isReferral(reply []byte, id uint16) (bool, error) {
	if len(reply) < dnsHeaderBytes || binary.BigEndian.Uint16(reply) != id {
		return false, errors.New("it answered something other than the question asked")
	}
	flags := binary.BigEndian.Uint16(reply[2:])
	if flags&dnsFlagResponse == 0 {
		return false, errors.New("it answered something other than the question asked")
	}
	switch rcode := flags & dnsRcodeMask; rcode {
	case 0:
	case dnsRcodeNameError:
		return false, nil
	default:
		return false, fmt.Errorf("it answered with rcode %d", rcode)
	}
	answers := binary.BigEndian.Uint16(reply[6:])
	authorities := binary.BigEndian.Uint16(reply[8:])
	return flags&dnsFlagAuthority == 0 && answers == 0 && authorities > 0, nil
}
