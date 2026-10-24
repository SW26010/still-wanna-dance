//go:build windows && cf_block_experiment

package console

import (
	"context"
	"errors"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamnet"
	"still-wanna-dance/internal/upstreamrequest"
)

type cfFamilyFailingDNS struct{ cfexpStatic }

func (d cfFamilyFailingDNS) Addresses(context.Context, string) ([]upstreamrequest.Address, error) {
	return nil, errors.New("controlled DNS failure")
}

func TestCFFamilyFailureHarness(t *testing.T) {
	const ip = "192.0.2.1"
	for _, tc := range []struct {
		name, kind string
		dns        upstreamrequest.DNSPool
	}{
		{"empty", "candidate_set_empty", cfexpStatic{}},
		{"removed", "chosen_candidate_not_returned", cfexpStatic{ips: []string{"192.0.2.2"}, until: time.Now().Add(time.Hour)}},
		{"lookup", "candidate_discovery_failed", cfFamilyFailingDNS{}},
		{"cancelled", "context", upstreamnet.NewResolver(nil)},
		{"binding", "network_binding_failed", cfexpStatic{}},
		{"available", "", cfexpStatic{ips: []string{ip}, until: time.Now().Add(time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, log := cfFixtureNetwork()
			defer n.cancel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.name == "cancelled" {
				cancel()
			}
			if tc.name == "binding" {
				n.fail(errors.New("adapter lost"))
			}
			tr, closePool, err := cfFamilyTransport(ctx, n, tc.dns, "https://"+cfexpHost+"/media?private=value", ip)
			defer closePool()
			if tc.kind == "" {
				if err != nil || tr == nil {
					t.Fatal("valid same IP rejected", err)
				}
				return
			}
			var detail *cfFamilyCandidateError
			if !errors.As(err, &detail) || detail.Kind != tc.kind || tr != nil {
				t.Fatal("failure classification", detail, err)
			}
			if tc.name == "lookup" && detail.Cause != "controlled DNS failure" {
				t.Fatal("lost DNS cause")
			}
			if tc.name == "removed" && (len(detail.Available) != 1 || detail.Available[0] != "192.0.2.2") {
				t.Fatal("lost returned candidates")
			}
			if tc.name == "cancelled" && (detail.Before == nil || detail.After == nil || detail.Before.Host != cfexpHost) {
				t.Fatal("lost before/after snapshots")
			}
			if log.Len() != 0 {
				t.Fatal("candidate failure must precede network download")
			}
		})
	}
}
