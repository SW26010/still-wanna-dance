package upstreamrequest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResourceDomainsFollowSourceReplacementExpiryAndChannel(t *testing.T) {
	c := NewChannel()
	if len(c.ResourceDomains()) != 0 {
		t.Fatal("predefined resource domains")
	}
	c.ObserveResourceDomains("api/one", []string{"media.future.example", "127.0.0.1"}, time.Minute)
	c.ObserveResourceDomains("api/two", []string{"media.future.example", "other.example"}, time.Minute)
	c.ObserveResourceDomains("api/one", []string{"replacement.example"}, time.Minute)
	if got := c.ResourceDomains(); !reflect.DeepEqual(got, []string{"media.future.example", "other.example", "replacement.example"}) {
		t.Fatal(got)
	}
	c.ObserveResourceDomains("api/two", nil, time.Minute)
	if c.IsResourceDomain("media.future.example") {
		t.Fatal("removed source retained membership")
	}
	c.mu.Lock()
	c.resourceDomains["api/one"] = resourceObservation{hosts: []string{"replacement.example"}, until: time.Now().Add(-time.Second)}
	c.mu.Unlock()
	if len(c.ResourceDomains()) != 0 {
		t.Fatal("expired source retained membership")
	}
	c.ObserveResourceDomains("api/one", []string{"new.example"}, time.Minute)
	previous := c.Snapshot().Revision
	c.Publish(nil)
	c.ObserveResourceDomainsAtRevision(previous, "late", []string{"retired.example"}, time.Minute)
	if len(c.ResourceDomains()) != 0 {
		t.Fatal("new channel inherited domains")
	}
}

func TestResourceSourcesPreserveActualNodeURLAndIndependentCopies(t *testing.T) {
	c := NewChannel()
	revision := c.Snapshot().Revision
	target := "http://nya.xin.moe/files/1/2-video.mp4?e=28711962048bed664c98f27e1d9d5842&s=4&token=private"
	cf := ResourceSource{API: "https://api.udon.dance/Api/Songs/play?node=cf&id=42", Node: "cf", SongID: 42, ResourceURL: target}
	nya := cf
	nya.API, nya.Node = "https://api.udon.dance/Api/Songs/play?node=nya&id=42", "nya"
	nya.ResourceURL += "&variant=nya"
	input := []ResourceSource{cf, nya}
	c.ObserveResourceSourcesAtRevision(revision, "batch", input, time.Minute)
	input[0].Node = "mutated"
	got := c.ResourceSources("nya.xin.moe")
	if len(c.ResourceDomains()) != 1 || len(got) != 2 || got[0].Node != "cf" || got[0].ResourceURL != target || got[1].Node != "nya" {
		t.Fatal(got)
	}
	got[0].Node = "mutated"
	if c.ResourceSources("nya.xin.moe")[0].Node != "cf" {
		t.Fatal("query mutated stored provenance")
	}
	b, err := json.Marshal(c.ResourceSources("nya.xin.moe"))
	if err != nil || strings.Contains(string(b), "private") || !strings.Contains(string(b), `"node":"cf"`) {
		t.Fatal("unsafe or incomplete status", string(b), err)
	}
	c.ObserveResourceSourcesAtRevision(revision, "batch", []ResourceSource{nya}, time.Minute)
	if got := c.ResourceSources(""); len(got) != 1 || got[0].Node != "nya" {
		t.Fatal("replacement retained stale source", got)
	}
	c.ObserveResourceSourcesAtRevision(revision, "batch", []ResourceSource{cf}, -time.Second)
	if len(c.ResourceSources("")) != 0 {
		t.Fatal("expired provenance retained")
	}
	c.Publish(nil)
	c.ObserveResourceSourcesAtRevision(revision, "late", []ResourceSource{cf}, time.Minute)
	if len(c.ResourceSources("")) != 0 {
		t.Fatal("retired API response accepted")
	}
}
