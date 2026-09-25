package console

import (
	"encoding/binary"
	"testing"
	"time"
)

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
