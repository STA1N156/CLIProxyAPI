package dataintegration

// estimateTokenCount avoids tokenizer work and is accurate enough for
// aggregate management statistics.
func estimateTokenCount(payload []byte) uint64 {
	return uint64((len(payload) + 3) / 4)
}
