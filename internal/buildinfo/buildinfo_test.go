package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestFormalLabelRequiresCleanVCSIdentity(t *testing.T) {
	old := Kind
	Kind = "formal"
	t.Cleanup(func() { Kind = old })
	for _, tc := range []struct{ name, revision, modified, want string }{
		{"clean", "0123456789012345678901234567890123456789", "false", "formal"},
		{"dirty", "0123456789012345678901234567890123456789", "true", "development"},
		{"unknown", "", "false", "development"},
		{"unknown status", "0123456789012345678901234567890123456789", "", "development"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := fromBuild(&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: tc.revision}, {Key: "vcs.modified", Value: tc.modified}}})
			if info.Kind != tc.want || info.Revision != tc.revision {
				t.Fatalf("unexpected identity: %+v", info)
			}
		})
	}
	if info := fromBuild(nil); info.Kind != "development" || !info.Dirty {
		t.Fatalf("unknown build claimed formal: %+v", info)
	}
}

func TestSourceRebuildRetainsOriginWithoutClaimingFormal(t *testing.T) {
	oldKind, oldRevision := Kind, SourceRevision
	Kind, SourceRevision = "rebuild", "0123456789012345678901234567890123456789"
	t.Cleanup(func() { Kind, SourceRevision = oldKind, oldRevision })
	info := fromBuild(nil)
	if info.Kind != "rebuild" || info.Revision != SourceRevision || info.Dirty {
		t.Fatalf("unexpected rebuild: %+v", info)
	}
	info = fromBuild(&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}})
	if !info.Dirty {
		t.Fatal("VCS dirty state was hidden by supplied source identity")
	}
}
