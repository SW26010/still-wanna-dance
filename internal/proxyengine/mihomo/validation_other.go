//go:build !windows

package mihomo

import "os/exec"

func hideValidationWindow(*exec.Cmd) {}
