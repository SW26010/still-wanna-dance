package desktop

import (
	"bytes"
	"strings"
	"testing"
)

func TestPathDialogRejectsInvalidRequest(t *testing.T) {
	for _, input := range []string{
		`{`, `null`, `{}`, `{"mode":"unknown"}`,
		`{"mode":"folder","extra":true}`,
		`{"mode":"folder","title":"bad\u0000title"}`,
		`{"mode":"folder","initial":"bad\u0000path"}`,
		`{"mode":"folder"} {}`, `{"mode":"folder"} trailing`,
		`{"mode":"folder","initial":"` + strings.Repeat("a", 4097) + `"}`,
	} {
		var output bytes.Buffer
		if err := RunPathDialog(strings.NewReader(input), &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid request returned success/output: %v, %q", err, output.String())
		}
	}
}
