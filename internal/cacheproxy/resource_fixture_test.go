package cacheproxy

// Explicit test-only resource origins; API nodes do not imply these names.
func fixtureConfig() Config {
	c := DefaultConfig()
	c.Origins["play.udon.dance"] = "play.udon.dance:443"
	c.Origins["nya.xin.moe"] = "nya.xin.moe:443"
	return c
}
