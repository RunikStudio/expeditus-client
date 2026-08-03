package browser

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/chromedp"
)

type Config struct {
	ExecPath      string
	Headless      bool
	NoSandbox     bool
	Timeout       time.Duration
	UserAgent     string
	WindowWidth   int
	WindowHeight  int
	DisableGPU    bool
	DisableDevShm bool
}

func DefaultConfig() Config {
	platform := DetectPlatform()
	noSandbox := platform == "linux"

	chromePath := AutoFindChromePath()

	return Config{
		ExecPath:      chromePath,
		Headless:      false,
		NoSandbox:     noSandbox,
		Timeout:       30 * time.Second,
		WindowWidth:   1920,
		WindowHeight:  1080,
		DisableGPU:    true,
		DisableDevShm: true,
	}
}

type Pool struct {
	mu           sync.RWMutex
	allocCtx     context.Context
	cancel       context.CancelFunc
	config       Config
	semaphore    chan struct{}  // Limits concurrent browsers to MaxBrowsers
	activeCount  int32          // Current number of active browsers
	MaxBrowsers int             // Maximum concurrent browsers (default 4)
}

func NewPool(ctx context.Context, cfg Config, maxBrowsers int) (*Pool, error) {
	opts := buildAllocatorOptions(cfg)

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	
	if maxBrowsers <= 0 {
		maxBrowsers = 4 // Default max 4 browsers
	}

	return &Pool{
		allocCtx:    allocCtx,
		cancel:     cancel,
		config:     cfg,
		semaphore:  make(chan struct{}, maxBrowsers),
		MaxBrowsers: maxBrowsers,
	}, nil
}

func (p *Pool) NewContext(parent context.Context) (context.Context, context.CancelFunc) {
	// Acquire semaphore (blocks if max browsers reached)
	p.semaphore <- struct{}{}
	atomic.AddInt32(&p.activeCount, 1)
	
	p.mu.RLock()
	defer p.mu.RUnlock()

	ctx, cancel := chromedp.NewContext(p.allocCtx)

	if p.config.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, p.config.Timeout)
	}

	// Create a custom cancel that also releases semaphore
	originalCancel := cancel
	cancel = func() {
		originalCancel()
		<-p.semaphore
		atomic.AddInt32(&p.activeCount, -1)
	}

	return ctx, cancel
}

func (p *Pool) ActiveBrowsers() int32 {
	return atomic.LoadInt32(&p.activeCount)
}

func (p *Pool) AvailableSlots() int {
	return p.MaxBrowsers - int(atomic.LoadInt32(&p.activeCount))
}

func (p *Pool) NewContextForWindow(parent context.Context, windowID string) (context.Context, context.CancelFunc) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	ctx, cancel := chromedp.NewContext(p.allocCtx)

	if p.config.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, p.config.Timeout)
	}

	return ctx, cancel
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cancel != nil {
		p.cancel()
	}
}

func buildAllocatorOptions(cfg Config) []chromedp.ExecAllocatorOption {
	opts := make([]chromedp.ExecAllocatorOption, 0, 12)
	opts = append(opts, chromedp.DefaultExecAllocatorOptions[:]...)

	if cfg.ExecPath != "" {
		opts = append(opts, chromedp.ExecPath(cfg.ExecPath))
	}

	opts = append(opts,
		chromedp.Flag("headless", cfg.Headless),
		chromedp.Flag("no-sandbox", cfg.NoSandbox),
		chromedp.Flag("disable-gpu", cfg.DisableGPU),
		chromedp.Flag("disable-dev-shm-usage", cfg.DisableDevShm),
		chromedp.Flag("disable-software-rasterizer", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-plugins", true),
		chromedp.Flag("disable-images", true),
		chromedp.WindowSize(cfg.WindowWidth, cfg.WindowHeight),
	)

	if cfg.UserAgent != "" {
		opts = append(opts, chromedp.UserAgent(cfg.UserAgent))
	}

	return opts
}

func (p *Pool) SingleRun(parent context.Context, tasks ...chromedp.Action) error {
	ctx, cancel := p.NewContext(parent)
	defer cancel()

	return chromedp.Run(ctx, tasks...)
}
