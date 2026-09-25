package console

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

func newDNSQuery(host string) ([]byte, error) {
	packet := make([]byte, 12)
	if _, err := rand.Read(packet[:2]); err != nil {
		return nil, err
	}
	packet[2] = 1
	packet[5] = 1
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("invalid DNS name")
		}
		packet = append(packet, byte(len(label)))
		packet = append(packet, label...)
	}
	packet = append(packet, 0, 0, 1, 0, 1)
	if len(packet) > 271 {
		return nil, fmt.Errorf("DNS name too long")
	}
	return packet, nil
}

func skipDNSName(b []byte, off int) (int, error) {
	for steps := 0; steps < 128; steps++ {
		if off >= len(b) {
			break
		}
		n := int(b[off])
		off++
		if n == 0 {
			return off, nil
		}
		if n&0xc0 == 0xc0 {
			if off >= len(b) {
				break
			}
			pointer := (n&63)<<8 | int(b[off])
			if pointer >= len(b) {
				break
			}
			return off + 1, nil
		}
		if n > 63 || off+n > len(b) {
			break
		}
		off += n
	}
	return 0, fmt.Errorf("malformed DNS name")
}

func parseDNSReply(query, b []byte) ([]string, time.Duration, error) {
	if len(query) < 12 || len(b) < len(query) || !bytes.Equal(b[:2], query[:2]) || b[2]&0x80 == 0 || b[2]&0x7a != 0 || b[3]&15 != 0 || binary.BigEndian.Uint16(b[4:6]) != 1 || !bytes.Equal(b[12:len(query)], query[12:]) {
		return nil, 0, fmt.Errorf("invalid or truncated DNS response")
	}
	off := len(query)
	answer := dnsAnswer{}
	for i := 0; i < int(binary.BigEndian.Uint16(b[6:8])); i++ {
		var err error
		off, err = skipDNSName(b, off)
		if err != nil {
			return nil, 0, err
		}
		if off+10 > len(b) {
			return nil, 0, fmt.Errorf("truncated DNS record")
		}
		kind := binary.BigEndian.Uint16(b[off:])
		class := binary.BigEndian.Uint16(b[off+2:])
		ttl := binary.BigEndian.Uint32(b[off+4:])
		size := int(binary.BigEndian.Uint16(b[off+8:]))
		off += 10
		if off+size > len(b) {
			return nil, 0, fmt.Errorf("truncated DNS data")
		}
		if kind == 1 && class == 1 && size == 4 {
			answer.Answer = append(answer.Answer, struct {
				Type int
				TTL  int
				Data string
			}{1, int(ttl), net.IP(b[off : off+4]).String()})
		}
		if kind == 5 && class == 1 {
			end, err := skipDNSName(b, off)
			if err != nil || end != off+size {
				return nil, 0, fmt.Errorf("malformed DNS alias")
			}
			answer.Answer = append(answer.Answer, struct {
				Type int
				TTL  int
				Data string
			}{5, int(ttl), ""})
		}
		off += size
	}
	return dnsAddresses(answer)
}
