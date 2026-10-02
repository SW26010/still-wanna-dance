package legal

import (
	"strings"
	"testing"
)

func TestExplicitVersionRequired(t *testing.T) {
	for _, v := range []string{"", "true", "old"} {
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
