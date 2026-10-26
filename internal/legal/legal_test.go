package legal

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestHashMatchesNoticeWithoutAllocation(t *testing.T) {
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(Text)))
	var got string
	allocs := testing.AllocsPerRun(100, func() { got = Hash() })
	if got != want {
		t.Fatalf("notice hash = %q, want %q", got, want)
	}
	if allocs != 0 {
		t.Fatalf("immutable notice hash allocated %v times per call", allocs)
	}
}

func TestExplicitVersionRequired(t *testing.T) {
	for _, v := range []string{"", "true", "old", "2026-10-11.1"} {
		if CheckAcceptance(v) == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	if CheckAcceptance(Version) != nil {
		t.Fatal("current version rejected")
	}
	if !strings.Contains(Text, "版本："+Version) {
		t.Fatal("text and acceptance version disagree")
	}
}
