package cacheproxy

import "testing"

func TestOriginAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		address string
		valid   bool
	}{
		{"play.udon.dance:443", true},
		{"origin.example:8443", true},
		{"127.0.0.1:18080", true},
		{"[::1]:18443", true},
		{"", false},
		{"play.udon.dance", false},
		{":443", false},
		{"play.udon.dance:0", false},
		{"play.udon.dance:65536", false},
		{"play.udon.dance:https", false},
		{"https://play.udon.dance:443", false},
	} {
		t.Run(tc.address, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Origins["play.udon.dance"] = tc.address
			if err := cfg.validate(); (err == nil) != tc.valid {
				t.Fatalf("validate(%q) = %v, valid=%v", tc.address, err, tc.valid)
			}
		})
	}
}
