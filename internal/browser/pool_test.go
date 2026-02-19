package browser

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func BenchmarkPoolNewContext(b *testing.B) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Timeout = 5 * time.Second

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		b.Fatalf("NewPool failed: %v", err)
	}
	defer pool.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		browserCtx, cancel := pool.NewContext(ctx)
		cancel()
		_ = browserCtx
	}
}

func BenchmarkPoolSingleRun(b *testing.B) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Timeout = 10 * time.Second

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		b.Fatalf("NewPool failed: %v", err)
	}
	defer pool.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var result string
		err := pool.SingleRun(ctx,
			chromedp.Navigate("about:blank"),
			chromedp.Evaluate(`document.title`, &result),
		)
		if err != nil {
			b.Fatalf("SingleRun failed: %v", err)
		}
	}
}

func BenchmarkConfigDefault(b *testing.B) {
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = DefaultConfig()
	}
}

func BenchmarkBuildAllocatorOptions(b *testing.B) {
	cfg := DefaultConfig()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = buildAllocatorOptions(cfg)
	}
}
