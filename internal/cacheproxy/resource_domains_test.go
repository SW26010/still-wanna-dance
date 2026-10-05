package cacheproxy

import (
	"net/http/httptest"
	"testing"
)

func TestResourceDomainRequiresValidatedResolverEvidence(t *testing.T) {
	s := &Server{cfg: DefaultConfig()}
	const target = "https://media.future.example/files/1/2-video.mp4?e=28711962048bed664c98f27e1d9d5842&s=4"
	if len(s.cfg.Origins) != 0 || s.resourceHostAllowed("media.future.example") {
		t.Fatal("predefined membership")
	}
	r := httptest.NewRequest("GET", target, nil)
	if _, err := s.parse(r); err != nil {
		t.Fatal(err)
	}
	if s.resourceHostAllowed(r.URL.Host) {
		t.Fatal("client URL created membership")
	}
	if _, err := s.parseResolved(r); err != nil {
		t.Fatal(err)
	}
	if !s.resourceHostAllowed(r.URL.Host) {
		t.Fatal("validated resolver URL did not establish membership")
	}
	bad := httptest.NewRequest("GET", "https://another.example/files/1/2-video.mp4?e=bad&s=4", nil)
	if _, err := s.parseResolved(bad); err == nil || s.resourceHostAllowed(bad.URL.Host) {
		t.Fatal("invalid API response established membership")
	}
}
