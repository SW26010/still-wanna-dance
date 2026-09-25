package console

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsStorageRoot(t *testing.T) {
	if _, err := absoluteSettings(Settings{}); err == nil {
		t.Fatal("empty storage root accepted")
	}
	s, err := absoluteSettings(Settings{StorageDir: "data"})
	if err != nil || !filepath.IsAbs(s.StorageDir) {
		t.Fatal(s, err)
	}
}

func TestCNAMECacheTTL(t *testing.T) {
	for _, ttl := range []int{0, 15, 180} {
		t.Run(fmt.Sprint(ttl), func(t *testing.T) {
			var answer dnsAnswer
			body := fmt.Sprintf(`{"Status":0,"Answer":[{"Type":5,"TTL":90,"Data":"alias.test"},{"Type":5,"TTL":%d,"Data":"target.test"},{"Type":1,"TTL":120,"Data":"203.0.113.9"}]}`, ttl)
			if err := json.Unmarshal([]byte(body), &answer); err != nil {
				t.Fatal(err)
			}
			ips, duration, err := dnsAddresses(answer)
			want := time.Duration(min(ttl, 90)) * time.Second
			if err != nil || len(ips) != 1 || duration != want {
				t.Fatal(ips, duration, err)
			}
		})
	}
}

func TestWireCNAMECacheTTL(t *testing.T) {
	q := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'a', 'p', 'i', 4, 't', 'e', 's', 't', 0, 0, 1, 0, 1}
	for _, ttl := range []byte{0, 15, 180} {
		reply := append([]byte(nil), q...)
		reply[2] = 0x81
		reply[3] = 0x80
		reply[7] = 2
		// The address precedes the alias, so TTL calculation must examine all answers.
		reply = append(reply, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 120, 0, 4, 198, 18, 0, 8)
		reply = append(reply, 0xc0, 12, 0, 5, 0, 1, 0, 0, 0, ttl, 0, 2, 0xc0, 12)
		ips, duration, err := parseDNSReply(q, reply)
		if err != nil || len(ips) != 1 || duration != time.Duration(min(ttl, 120))*time.Second {
			t.Fatal(ips, duration, err)
		}
	}
}
