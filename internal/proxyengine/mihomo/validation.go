package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/sirupsen/logrus"
)

const validationArgument = "--swd-mihomo-check"

// A private entry point also works in test executables, before TestMain starts
// fixtures. Credentials travel through stdin, never arguments or environment.
func init() {
	logrus.SetOutput(io.Discard)
	if len(os.Args) != 2 || os.Args[1] != validationArgument {
		return
	}
	var input struct {
		Data []byte
		Home string
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 3*MaxImportBytes)).Decode(&input) != nil {
		os.Exit(1)
	}
	raw, _, err := managedConfiguration(input.Data, false)
	clear(input.Data)
	if err != nil {
		os.Exit(1)
	}
	C.SetHomeDir(input.Home)
	if _, err := config.ParseRawConfig(raw); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// Validate invokes the same managed configuration policy and native parser as
// startup, without applying a configuration to the running core. Native resource
// loading may use the network. Its caches remain owned by Mihomo.
func Validate(ctx context.Context, data []byte, home string) (Summary, error) {
	summary, err := Inspect(data)
	if err != nil {
		return Summary{}, err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return Summary{}, ErrConfiguration
	}
	exe, err := os.Executable()
	if err != nil {
		return Summary{}, ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	input, _ := json.Marshal(struct {
		Data []byte
		Home string
	}{data, home})
	defer clear(input)
	cmd := exec.CommandContext(ctx, exe, validationArgument)
	hideValidationWindow(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(input), io.Discard, io.Discard
	if cmd.Run() != nil {
		if ctx.Err() != nil {
			return Summary{}, ctx.Err()
		}
		return Summary{}, ErrConfiguration
	}
	return summary, nil
}
