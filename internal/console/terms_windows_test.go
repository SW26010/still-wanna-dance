package console

import (
	"os"
	"os/exec"
	"testing"
)

// Run the actual OS implementation in a fresh process under the same user.
// Cross-user/machine and roaming-profile behavior still require separate hosts.
func TestTermsDPAPIFreshProcess(t *testing.T) {
	if config := os.Getenv("SWD_TEST_TERMS_CONFIG"); config != "" {
		c := &Console{configPath: config}
		if err := c.loadTerms(); err != nil {
			t.Fatal(err)
		}
		if !c.termsAccepted() {
			t.Fatal("fresh process could not validate user receipt")
		}
		return
	}
	c := testConsole(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTermsDPAPIFreshProcess$")
	cmd.Env = append(os.Environ(), "SWD_TEST_TERMS_CONFIG="+c.configPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}
