package console

import (
	"still-wanna-dance/internal/upstreamrequest"
	"testing"
)

func TestUpstreamChannelFollowsSettings(t *testing.T) {
	c := testConsole(t)
	before := upstreamrequest.Default.Snapshot()
	if before.Candidates != c.client.Transport.(upstreamrequest.Candidates) {
		t.Fatal("initial request channel differs")
	}
	s := c.settings
	s.UpstreamMode = "proxy"
	s.SOCKS5Address = "127.0.0.1:1080"
	if err := c.saveSettings(s, false, false); err != nil {
		t.Fatal(err)
	}
	after := upstreamrequest.Default.Snapshot()
	if after.Candidates != c.client.Transport.(upstreamrequest.Candidates) || after.Revision != before.Revision {
		t.Fatal("save changed active channel")
	}
	select {
	case <-before.Changed:
		t.Fatal("save announced runtime change")
	default:
	}
	c.Close()
	if upstreamrequest.Default.Snapshot().Candidates != nil {
		t.Fatal("closed console retained channel")
	}
}
