package cacheproxy

// Explicit test-only resource origins; API nodes do not imply these names.
func fixtureConfig() Config {
	c := DefaultConfig()
	c.CacheUnlimited = true // Cache reuse tests explicitly retain videos between requests.
	c.Origins["play.udon.dance"] = "play.udon.dance:443"
	c.Origins["nya.xin.moe"] = "nya.xin.moe:443"
	return c
}
