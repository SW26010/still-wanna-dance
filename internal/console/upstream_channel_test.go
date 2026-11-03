package console

import (
	"testing"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestUpstreamChannelFollowsSettings(t *testing.T) {
	c := testConsole(t)
	before := c.requestChannel.Snapshot()
	if before.Candidates != c.client.Transport.(upstreamrequest.Candidates) {
		t.Fatal("initial request channel differs")
	}
	s := c.settings
	s.UpstreamMode = "proxy"
	s.SOCKS5Address = "127.0.0.1:1080"
	if err := c.saveSettings(s, false, nil); err != nil {
		t.Fatal(err)
	}
	after := c.requestChannel.Snapshot()
	if after.Candidates != c.client.Transport.(upstreamrequest.Candidates) || after.Revision != before.Revision {
		t.Fatal("save changed active channel")
	}
	select {
	case <-before.Changed:
		t.Fatal("save announced runtime change")
	default:
	}
	c.Close()
	if c.requestChannel.Snapshot().Candidates != nil {
		t.Fatal("closed console retained channel")
	}
}

func TestUpstreamChannelsAreIsolatedPerConsole(t *testing.T) {
	for _, closeFirst := range []string{"first", "second"} {
		t.Run(closeFirst, func(t *testing.T) {
			t.Parallel()
			first := testConsole(t)
			testDNS(first).seed("api.udon.dance", "203.0.113.11")
			var err error
			first.monitor, err = first.newUpstreamMonitor(upstreamstate.Options{})
			if err != nil {
				t.Fatal(err)
			}
			firstBefore := first.requestChannel.Snapshot()

			second := testConsole(t)
			testDNS(second).seed("api.udon.dance", "203.0.113.22")
			second.monitor, err = second.newUpstreamMonitor(upstreamstate.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if after := first.requestChannel.Snapshot(); after.Revision != firstBefore.Revision || after.Candidates != firstBefore.Candidates {
				t.Fatal("another console replaced the first publication")
			}
			select {
			case <-firstBefore.Changed:
				t.Fatal("another console announced a change")
			default:
			}

			catalogIP := func(c *Console) string {
				for _, result := range c.monitor.Results(upstreamstate.Catalog) {
					if result.Route == "api" {
						return result.IP
					}
				}
				return ""
			}
			if got := catalogIP(first); got != "203.0.113.11" {
				t.Fatalf("first monitor observed %q", got)
			}
			if got := catalogIP(second); got != "203.0.113.22" {
				t.Fatalf("second monitor observed %q", got)
			}

			closing, survivor, survivorIP := first, second, "203.0.113.22"
			if closeFirst == "second" {
				closing, survivor, survivorIP = second, first, "203.0.113.11"
			}
			before := survivor.requestChannel.Snapshot()
			if err := closing.Close(); err != nil {
				t.Fatal(err)
			}
			if closing.requestChannel.Snapshot().Candidates != nil || !closing.monitor.Snapshot().Closed {
				t.Fatal("closing console retained its channel or monitor")
			}
			if after := survivor.requestChannel.Snapshot(); after.Revision != before.Revision || after.Candidates != before.Candidates || catalogIP(survivor) != survivorIP {
				t.Fatal("closing one console changed the survivor")
			}
			select {
			case <-before.Changed:
				t.Fatal("closing one console notified the survivor")
			default:
			}

			testDNS(survivor).seed("api.udon.dance", "203.0.113.33")
			survivor.requestRevision = survivor.requestChannel.Publish(survivor.client.Transport.(upstreamrequest.Candidates))
			if got := catalogIP(survivor); got != "203.0.113.33" {
				t.Fatalf("survivor did not observe its new publication: %q", got)
			}
			if closing.requestChannel.Snapshot().Candidates != nil {
				t.Fatal("survivor republished the closed console's channel")
			}
			if err := survivor.Close(); err != nil {
				t.Fatal(err)
			}
			if survivor.requestChannel.Snapshot().Candidates != nil || !survivor.monitor.Snapshot().Closed {
				t.Fatal("surviving console did not release its channel and monitor")
			}
		})
	}
}
