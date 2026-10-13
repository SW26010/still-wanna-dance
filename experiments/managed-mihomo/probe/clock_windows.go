//go:build historical_a

package main

import (
	"syscall"
	"unsafe"
)

// QPC preserves sub-millisecond measurements on this Windows Go baseline.
var kernel = syscall.NewLazyDLL("kernel32.dll")
var counter = kernel.NewProc("QueryPerformanceCounter")
var frequency = func() float64 {
	var n int64
	kernel.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&n)))
	return float64(n)
}()

func clockSeconds() float64 {
	var n int64
	counter.Call(uintptr(unsafe.Pointer(&n)))
	return float64(n) / frequency
}
