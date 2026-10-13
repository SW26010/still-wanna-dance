//go:build historical_a && !windows

package main

import "time"

var origin = time.Now()

func clockSeconds() float64 { return time.Since(origin).Seconds() }
