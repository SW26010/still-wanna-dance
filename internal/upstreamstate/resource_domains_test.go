package upstreamstate

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type domainCandidates struct {
	*candidateFixture
	wrap func(http.RoundTripper) http.RoundTripper
}

func (f *domainCandidates) Current(target string) []upstreamrequest.Candidate {
	cs := f.candidateFixture.Current(target)
	for i := range cs {
		cs[i].Transport = f.wrap(cs[i].Transport)
	}
	return cs
}

func (f *domainCandidates) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return f.Current(target), nil
}

func TestResourcesFollowReturnedDomainsAcrossNodesAndChannels(t *testing.T) {
	for _, hosts := range []struct{ name, cf, nya string }{
		{"both play", "play.udon.dance", "play.udon.dance"},
		{"both nya", "nya.xin.moe", "nya.xin.moe"},
		{"new domains", "media.future.example", "video.other.example"},
		{"swapped", "nya.xin.moe", "play.udon.dance"},
	} {
		for _, candidates := range []bool{false, true} {
			name := hosts.name + "/ordinary"
			if candidates {
				name = hosts.name + "/candidates"
			}
			t.Run(name, func(t *testing.T) {
				var mu sync.Mutex
				loads := make(map[string]int)
				wrap := func(base http.RoundTripper) http.RoundTripper {
					return transportFunc(func(r *http.Request) (*http.Response, error) {
						if r.URL.Path == "/Api/Songs/play" {
							host := hosts.cf
							if r.URL.Query().Get("node") == "nya" {
								host = hosts.nya
							}
							resp := response(302, "")
							resp.Header.Set("Location", strings.Replace(videoFixture, "play.udon.dance", host, 1))
							return resp, nil
						}
						if r.Header.Get("Range") != "" {
							mu.Lock()
							loads[r.URL.Hostname()]++
							mu.Unlock()
						}
						return base.RoundTrip(r)
					})
				}
				ch := upstreamrequest.NewChannel()
				wantLoads := 1
				if candidates {
					ch.Publish(&domainCandidates{candidateFixture: &candidateFixture{expires: time.Now().Add(time.Hour), second: true, changed: make(chan struct{})}, wrap: wrap})
					wantLoads = 2
				} else {
					ch.Publish(wrap(transportFunc(fixture)))
				}
				m, err := newMonitor(Options{}, ch)
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close()
				if err := m.Check(context.Background()); err != nil {
					t.Fatal(err)
				}
				for _, r := range m.Results(PlaybackURL) {
					if r.State != "available" {
						t.Fatal("node rejected a supported returned domain", r)
					}
				}
				sources := m.ResourceSources("")
				if len(sources) != 2*wantLoads {
					t.Fatalf("lost API sources: %+v", sources)
				}
				if got := ch.ResourceSources(""); len(got) != len(sources) {
					t.Fatalf("channel lost API sources: %+v", got)
				}
				for _, source := range sources {
					host := hosts.cf
					if source.Node == "nya" {
						host = hosts.nya
					} else if source.Node != "cf" {
						t.Fatal(source)
					}
					if source.Domain != host || source.ResourceURL != strings.Replace(videoFixture, "play.udon.dance", host, 1) || !strings.Contains(source.API, "node="+source.Node) || source.SongID <= 0 || source.ObservedAt.IsZero() || !source.ValidUntil.After(source.ObservedAt) {
						t.Fatalf("incorrect provenance: %+v", source)
					}
					if candidates && source.APIChannelID == "" {
						t.Fatal("missing issuing API channel")
					}
				}
				wantDomains := 2
				if hosts.cf == hosts.nya {
					wantDomains = 1
				}
				if got := len(m.Results(Resource)); got != wantDomains*wantLoads {
					t.Fatalf("resource count = %d, want %d", got, wantDomains*wantLoads)
				}
				for _, r := range m.Results(Resource) {
					if len(r.Sources) == 0 {
						t.Fatal("status lost sources", r)
					}
					returned := r.Route == hosts.cf || r.Route == hosts.nya
					if r.Entry != "https://"+r.Route || (r.Route != hosts.cf && r.Route != hosts.nya) {
						t.Fatal("resource identity is not its domain", r)
					}
					if returned && (r.State != "available" || loads[r.Route] != wantLoads) {
						t.Fatal("resource omitted, mislabeled or probed twice", r, loads)
					}
					if !returned && (r.State != "unknown" || loads[r.Route] != 0) {
						t.Fatal("fabricated a resource from a node name", r, loads)
					}
				}
				if candidates {
					s, ok := m.Recommended(Resource)
					if !ok || s.Channel.Host != s.Result.Route || s.Result.Entry != "https://"+s.Channel.Host {
						t.Fatal("recommended channel belongs to another domain", s, ok)
					}
				}
			})
		}
	}
}

func TestResourceSetReplacesAndExpiresAPIResults(t *testing.T) {
	ch := upstreamrequest.NewChannel()
	m, err := newMonitor(Options{}, ch)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	now := time.Now()
	m.record(observation{op: PlaybackURL, route: "hkg", resourceHost: "first.example", state: "available", at: now})
	m.record(observation{op: PlaybackURL, route: "cf", resourceHost: "shared.example", state: "available", at: now})
	m.record(observation{op: PlaybackURL, route: "hkg", resourceHost: "new.example", state: "available", at: now.Add(time.Millisecond)})
	if got := m.Results(Resource); len(got) != 2 {
		t.Fatal(got)
	}
	if ch.IsResourceDomain("first.example") || !ch.IsResourceDomain("new.example") {
		t.Fatal(ch.ResourceDomains())
	}
	m.record(observation{op: PlaybackURL, route: "cf", state: "unavailable", at: now.Add(time.Millisecond)})
	if got := m.Results(Resource); len(got) != 1 || got[0].Route != "new.example" {
		t.Fatal(got)
	}
	m.mu.Lock()
	for key, source := range m.resourceSources {
		source.at = now.Add(-time.Hour)
		m.resourceSources[key] = source
	}
	m.mu.Unlock()
	if got := m.Results(Resource); len(got) != 0 {
		t.Fatal("expired domains remain", got)
	}
}

func TestRetiredAPISourcesDisappearBeforeAnotherCheckCompletes(t *testing.T) {
	m, f := candidateMonitor(t)
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.channel.ResourceSources(""); len(got) != 4 {
		t.Fatal(got)
	}
	f.mu.Lock()
	f.second = false
	f.mu.Unlock()
	// Read shared state first: no monitor snapshot or completed check can clean it for us.
	if got := m.channel.ResourceSources(""); len(got) != 2 {
		t.Fatal("shared set retained retired API source", got)
	}
	if got := m.ResourceSources(""); len(got) != 2 {
		t.Fatal("monitor retained retired API source", got)
	}
	for _, r := range m.Results(Resource) {
		if len(r.Sources) != 1 || strings.HasPrefix(r.Sources[0].APIChannelID, "b/") {
			t.Fatal(r)
		}
	}
	f.mu.Lock()
	f.second = true
	f.mu.Unlock()
	if len(m.ResourceSources("")) != 2 || len(m.channel.ResourceSources("")) != 2 {
		t.Fatal("retired source revived without a new API response")
	}
	f.mu.Lock()
	f.expires = time.Now().Add(-time.Second)
	f.mu.Unlock()
	if got := m.channel.ResourceDomains(); len(got) != 0 {
		t.Fatal("shared domain outlived all issuers", got)
	}
	if got := m.Results(Resource); len(got) != 0 {
		t.Fatal("monitor domain outlived all issuers", got)
	}
}
