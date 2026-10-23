package blocksched

// Comparable completions supply diagnostics and calibration counts only.
// Tail blocks and sequential fallbacks are not standard blocks.
func (p *Progress) comparable(sampleBytes int64) bool {
	return sampleBytes > 0 && p.work == sampleBytes && !p.admitted.IsZero() && p.ready.After(p.admitted)
}
