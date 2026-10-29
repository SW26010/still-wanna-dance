package console

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestChannelModesFollowSettingsAndRetireSavedCandidates(t *testing.T) {
	c := testConsole(t)
	testDNS(c).seed("api.udon.dance", "203.0.113.1")
	target := "https://api.udon.dance/Api/Songs/list"
	initial := upstreamrequest.Default.Snapshot()
	direct := initial.Candidates.Current(target)
	if len(direct) != 1 || direct[0].Mode != "direct" {
		t.Fatal(direct)
	}
	s := c.settings
	s.UpstreamMode = "proxy"
	s.SOCKS5Address = "127.0.0.1:1080"
	if err := c.saveSettings(s, false, false); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	testDNS(c).seed("api.udon.dance", "203.0.113.1")
	r, _ := http.NewRequest("GET", target, nil)
	if _, err := direct[0].Transport.RoundTrip(r); !errors.Is(err, upstreamrequest.ErrUnavailable) {
		t.Fatal("old candidate survived config replacement", err)
	}
	proxy, err := upstreamrequest.Default.Snapshot().Candidates.Candidates(context.Background(), "https://never-resolve.invalid")
	if err != nil || len(proxy) != 1 || proxy[0].Mode != "socks5" {
		t.Fatal(proxy, err)
	}
	if testDNS(c).queries.Load() != 0 {
		t.Fatal("forced SOCKS5 invoked local DNS")
	}
}
