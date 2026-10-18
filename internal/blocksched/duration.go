package blocksched

const standardBlockSize = 1 << 20

// Comparable completions supply diagnostics and calibration counts only.
// Tail blocks and sequential fallbacks are not standard blocks.
func (p *Progress) comparable() bool {
	return p.work == standardBlockSize && !p.admitted.IsZero() && p.ready.After(p.admitted)
}
