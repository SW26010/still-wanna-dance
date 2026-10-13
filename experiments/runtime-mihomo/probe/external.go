//go:build external_only

package main

import (
	"errors"
	"still-wanna-dance/internal/upstreamrequest"
)

type fullRuntime struct{ proxies []upstreamrequest.Proxy }

func prepare(string) ([]map[string]any, error)     { return nil, errors.New("prepare-in-B-helper-only") }
func startRuntime(command) (*fullRuntime, error)   { return nil, errors.New("external-only") }
func (*fullRuntime) Close()                        {}
func (*fullRuntime) ShutdownOnly()                 {}
func (*fullRuntime) snapshot() map[string]any      { return nil }
func (*fullRuntime) bindingChecks() map[string]any { return nil }
func (*fullRuntime) swapNames() bool               { return false }
