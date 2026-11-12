//go:build !race

package cacheproxy

import "testing"

// File-opening allocations must scale with blocks, rather than Read calls.
// Comparing buffer sizes avoids a wall-clock threshold and platform-specific
// allocation counts. Race instrumentation adds per-read allocations, so this
// performance assertion runs separately from the reader's lifecycle/race tests.
func TestBlockReadAllocationsScaleWithBlocks(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "stream"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			blocks := storedTestBlocks(t, 2)
			large, small := make([]byte, rangeBlockSize), make([]byte, 32*1024)
			perBlock := testing.AllocsPerRun(5, func() { readTestBlocks(t, blocks, snapshot, large) })
			perChunk := testing.AllocsPerRun(5, func() { readTestBlocks(t, blocks, snapshot, small) })
			if perChunk > perBlock+2 {
				t.Fatalf("32 KiB reads allocated %.0f times, 1 MiB reads %.0f; file lifetime scales with chunks", perChunk, perBlock)
			}
		})
	}
}
