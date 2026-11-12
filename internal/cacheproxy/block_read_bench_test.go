package cacheproxy

import (
	"context"
	"io"
	"testing"
)

func readTestBlocks(tb testing.TB, blocks *blockFiles, snapshot bool, buffer []byte) {
	tb.Helper()
	var reader io.ReadCloser
	if snapshot {
		view, err := blocks.snapshot()
		if err != nil {
			tb.Fatal(err)
		}
		defer view.Close()
		reader = io.NopCloser(io.NewSectionReader(view, 0, blocks.size))
	} else {
		spool := (&localResource{storage: blocks}).makeSpool(nil)
		defer spool.release()
		reader = spool.reader(context.Background(), blocks.size)
	}
	defer reader.Close()
	var total int64
	for {
		n, err := reader.Read(buffer)
		total += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			tb.Fatal(err)
		}
	}
	if total != blocks.size {
		tb.Fatalf("read %d bytes, want %d", total, blocks.size)
	}
}

func storedTestBlocks(tb testing.TB, count int64) *blockFiles {
	tb.Helper()
	blocks := newBlockFiles(tb.TempDir(), count*rangeBlockSize, rangeBlockSize, nil)
	tb.Cleanup(blocks.release)
	body := make([]byte, rangeBlockSize)
	for block := range count {
		if err := blocks.store(block, body); err != nil {
			tb.Fatal(err)
		}
	}
	return blocks
}

func BenchmarkBlockRead(b *testing.B) {
	for _, snapshot := range []bool{false, true} {
		name := "stream"
		if snapshot {
			name = "snapshot"
		}
		b.Run(name, func(b *testing.B) {
			blocks := storedTestBlocks(b, 64)
			buffer := make([]byte, 32*1024)
			readTestBlocks(b, blocks, snapshot, buffer) // Warm the OS cache.
			b.SetBytes(blocks.size)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				readTestBlocks(b, blocks, snapshot, buffer)
			}
		})
	}
}
