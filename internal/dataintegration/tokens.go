package dataintegration

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var (
	tokenEncoderOnce sync.Once
	tokenEncoderMu   sync.Mutex
	tokenEncoder     tokenizer.Codec
)

// estimateTokenCount uses one consistent tokenizer across supported request formats.
func estimateTokenCount(payload []byte) uint64 {
	tokenEncoderOnce.Do(func() {
		tokenEncoder, _ = tokenizer.Get(tokenizer.O200kBase)
	})
	if tokenEncoder == nil {
		return uint64((len(payload) + 3) / 4)
	}
	tokenEncoderMu.Lock()
	count, errCount := tokenEncoder.Count(string(payload))
	tokenEncoderMu.Unlock()
	if errCount != nil || count < 0 {
		return uint64((len(payload) + 3) / 4)
	}
	return uint64(count)
}
