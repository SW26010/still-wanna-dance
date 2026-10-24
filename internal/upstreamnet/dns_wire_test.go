package upstreamnet

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestDNSWireAAAA(t *testing.T) {
	for _, tc := range []struct {
		ip string
		ok bool
	}{
		{"2606:4700:20::681a:41c", true},
		{"::", false}, {"::1", false}, {"fe80::1", false}, {"fd00::1", false}, {"ff02::1", false}, {"::ffff:192.0.2.1", false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			query, err := newDNSQuery("example.test", dnsTypeAAAA)
			if err != nil || binary.BigEndian.Uint16(query[len(query)-4:]) != 28 {
				t.Fatal("invalid AAAA query", err)
			}
			reply := append([]byte(nil), query...)
			reply[2], reply[3], reply[7] = 0x81, 0x80, 2
			// The alias expires sooner than the address.
			reply = append(reply, 0xc0, 12, 0, 5, 0, 1, 0, 0, 0, 10, 0, 2, 0xc0, 12)
			recordOffset := len(reply)
			reply = append(reply, 0xc0, 12, 0, 28, 0, 1, 0, 0, 0, 60, 0, 16)
			reply = append(reply, net.ParseIP(tc.ip).To16()...)
			ips, ttl, err := parseDNSReply(query, reply)
			if tc.ok {
				if err != nil || len(ips) != 1 || ips[0] != tc.ip || ttl != 10*time.Second {
					t.Fatal(ips, ttl, err)
				}
				if _, _, err := parseDNSReply(query, reply[:len(reply)-1]); err == nil {
					t.Fatal("truncated AAAA accepted")
				}
				reply[recordOffset+3] = 1
				if _, _, err := parseDNSReply(query, reply); err == nil {
					t.Fatal("wrong address type accepted")
				}
			} else if err == nil {
				t.Fatal("non-public or mapped AAAA accepted", ips)
			}
		})
	}
}

func TestDNSWireValidation(t *testing.T) {
	q := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'a', 'p', 'i', 4, 't', 'e', 's', 't', 0, 0, 1, 0, 1}
	reply := append([]byte(nil), q...)
	reply[2] = 0x81
	reply[3] = 0x80
	reply[7] = 1
	reply = append(reply, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 198, 18, 0, 8)
	ips, ttl, err := parseDNSReply(q, reply)
	if err != nil || len(ips) != 1 || ips[0] != "198.18.0.8" || ttl != time.Minute {
		t.Fatal(ips, ttl, err)
	}
	for i := 0; i < len(reply); i++ {
		if _, _, err := parseDNSReply(q, reply[:i]); err == nil {
			t.Fatalf("accepted truncated packet at %d", i)
		}
	}
	for _, offset := range []int{0, 2, 12, 25} {
		bad := append([]byte(nil), reply...)
		bad[offset] ^= 0xff
		if _, _, err := parseDNSReply(q, bad); err == nil {
			t.Fatalf("accepted invalid packet byte %d", offset)
		}
	}
	bad := append([]byte(nil), reply...)
	binary.BigEndian.PutUint16(bad[len(q)+10:], 65535)
	if _, _, err := parseDNSReply(q, bad); err == nil {
		t.Fatal("accepted oversized resource")
	}
}
