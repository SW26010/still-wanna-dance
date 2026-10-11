package apppaths

import "testing"

func TestConfigCannotOwnAnotherInstancesWorkSlot(t *testing.T) {
	for _, name := range []string{".config.json.swd-tmp", ".a.json.SWD-TMP"} {
		if err := ValidateConfigPath(name); err == nil {
			t.Fatalf("accepted reserved work slot %s", name)
		}
	}
	if err := ValidateConfigPath("a.json"); err != nil {
		t.Fatal(err)
	}
}
