package console

import (
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
)

func initializeTestLibrary(t *testing.T, c *Console) {
	t.Helper()
	cfg := fixtureCacheConfig()
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	s, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func observeTestResources(t *testing.T, resources *upstreamrequest.Resources) {
	t.Helper()
	source := "test/" + t.Name()
	resources.ObserveResourceDomains(source, []string{"play.udon.dance", "nya.xin.moe"}, time.Minute)
	t.Cleanup(func() { resources.ObserveResourceDomains(source, nil, 0) })
}

// Explicit test-only resource origins; API nodes do not imply these names.
func fixtureCacheConfig() cacheproxy.Config {
	c := cacheproxy.DefaultConfig()
	c.CacheUnlimited = true // Cache reuse tests explicitly retain videos between requests.
	c.Origins["play.udon.dance"] = "play.udon.dance:443"
	c.Origins["nya.xin.moe"] = "nya.xin.moe:443"
	return c
}
