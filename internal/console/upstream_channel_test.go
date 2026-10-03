package console

import (
	"still-wanna-dance/internal/upstreamrequest"
	"testing"
)

func TestUpstreamChannelFollowsSettings(t *testing.T) {
	c := testConsole(t)
	before := upstreamrequest.Default.Snapshot()
	if before.Transport != c.client.Transport {
		t.Fatal("initial request channel differs")
	}
	s := c.settings
	s.UpstreamMode = "socks5"
	s.SOCKS5Address = "127.0.0.1:1080"
	if err := c.saveSettings(s, false); err != nil {
		t.Fatal(err)
	}
	after := upstreamrequest.Default.Snapshot()
	if after.Transport != c.client.Transport || after.Revision == before.Revision {
		t.Fatal("channel did not follow SOCKS settings")
	}
	select {
	case <-before.Changed:
	default:
		t.Fatal("change not announced")
	}
	c.Close()
	if upstreamrequest.Default.Snapshot().Transport != nil {
		t.Fatal("closed console retained channel")
	}
}
