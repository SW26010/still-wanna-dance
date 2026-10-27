package console

import (
	"strings"
	"testing"
)

func TestHostsPreservesOtherMarkers(t *testing.T) {
	original := "127.0.0.1 localhost\r\n\r\n127.0.0.1 play.udon.dance # SWD managed\r\n\r\n127.0.0.1 nya.xin.moe # StepStash managed\r\n# SWD managed: unrelated note\r\n"
	enabled, err := transformHosts(original, "enable")
	if err != nil || !strings.Contains(enabled, marker) {
		t.Fatal(enabled, err)
	}
	restored, err := transformHosts(enabled, "disable")
	if err != nil || restored != original {
		t.Fatal(restored, err)
	}
	for _, edited := range []string{
		"127.0.0.2 api.udon.dance " + marker,
		"127.0.0.1\tAPI.UDON.DANCE. " + marker,
		"127.0.0.1 another.example api.udon.dance " + marker,
	} {
		if !hasManagedHosts(edited) {
			t.Fatalf("modified API entry lost ownership: %q", edited)
		}
		if _, err := transformHosts(edited, "disable"); err == nil {
			t.Fatalf("modified API entry accepted: %q", edited)
		}
	}
}
