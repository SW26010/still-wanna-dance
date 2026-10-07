//go:build !windows

package main

import "os/exec"

func configureRestartCommand(*exec.Cmd) {}
