package services

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sync"
	"time"

	"ExpeditusClient/internal/browser"
	"ExpeditusClient/internal/config"
	"ExpeditusClient/internal/web/models"
	"ExpeditusClient/internal/web/ws"

	"github.com/chromedp/chromedp"
)

const (
	searchURL      = "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&&departureDate=09/05/2026&arrivalDate=23/05/2026&hotelDestination=Destination::AUA"
	defaultTimeout = 60 * time.Second
)

type ScraperService struct {
	mu       sync.RWMutex
	sessions map[string]*ScraperSession
	pool     *browser.Pool
}

type ScraperSession struct {
	ID        string
	Ctx       context.Context
	Cancel    context.CancelFunc
	Wg        sync.WaitGroup
	Progress  models.ProgressUpdate
	Results   []models.Result
	Status    string
	Error     error
	StartTime time.Time
	EndTime   *time.Time
}

func NewScraperService() (*ScraperService, error) {
	ctx := context.Background()

	browserCfg := browser.DefaultConfig()
	browserCfg.Timeout = defaultTimeout
	browserCfg.Headless = true

	pool, err := browser.NewPool(ctx, browserCfg)
	if err != nil {
		return nil, fmt.Errorf("creating browser pool: %w", err)
	}

	return &ScraperService{
		sessions: make(map[string]*ScraperSession),
		pool:     pool,
	}, nil
}

func (s *ScraperService) StartSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[sessionID]; exists {
		return fmt.Errorf("session %s already exists", sessionID)
	}

	cfg, err := config.LoadLoginConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &ScraperSession{
		ID:        sessionID,
		Ctx:       ctx,
		Cancel:    cancel,
		Status:    models.SessionStatusRunning,
		StartTime: time.Now(),
		Progress:  *models.NewProgressUpdate(sessionID),
	}

	session.Progress.SetStage(models.StageLogin)
	s.sessions[sessionID] = session

	go s.runScraping(session, cfg)

	return nil
}

func (s *ScraperService) runScraping(session *ScraperSession, cfg *config.LoginConfig) {
	defer func() {
		session.Wg.Done()
		s.mu.Lock()
		if session.EndTime == nil {
			now := time.Now()
			session.EndTime = &now
		}
		s.mu.Unlock()
	}()

	session.Wg.Add(1)

	s.sendProgress(session, 5, models.StageLogin, "Iniciando navegador...")

	browserCtx, cancel := s.pool.NewContext(session.Ctx)
	defer cancel()

	session.Progress.SetStage(models.StageLogin)
	s.sendProgress(session, 10, models.StageLogin, "Navegando a Delfos...")

	var sessionID, currentURL string
	var debugLog string

	err := chromedp.Run(browserCtx,
		chromedp.Navigate(cfg.TargetURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		s.handleError(session, fmt.Errorf("navigation failed: %w", err))
		return
	}

	s.sendProgress(session, 20, models.StageLogin, "Haciendo click en Entrar...")

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			const links = document.querySelectorAll('a, button');
			for (const link of links) {
				if (link.textContent?.trim().toLowerCase() === 'entrar') {
					link.click();
					return 'clicked-entrar';
				}
			}
			return 'not-found';
		})()`, nil),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		s.handleError(session, fmt.Errorf("click entrar failed: %w", err))
		return
	}

	s.sendProgress(session, 30, models.StageLogin, "Completando formulario de login...")

	fillScript := fmt.Sprintf(`(() => {
		const debug = [];
		
		let loginForm = null;
		const forms = document.querySelectorAll('form');
		for (const form of forms) {
			const hasPassword = form.querySelector('input[type="password"]');
			const hasEmail = form.querySelector('input[type="text"]');
			if (hasPassword && hasEmail) {
				loginForm = form;
				debug.push('Found login form');
				break;
			}
		}
		
		const searchRoot = loginForm || document;
		
		let emailInput = null;
		let passwordInput = null;
		let submitButton = null;
		
		const passwordInputs = searchRoot.querySelectorAll('input[type="password"]');
		if (passwordInputs.length > 0) {
			passwordInput = passwordInputs[0];
		}
		
		const textInputs = searchRoot.querySelectorAll('input[type="text"]');
		for (const input of textInputs) {
			const id = (input.id || '').toLowerCase();
			const name = (input.name || '').toLowerCase();
			if (id.includes('email') || name.includes('email')) {
				emailInput = input;
				debug.push('Found email by id/name: ' + input.id);
				break;
			}
		}
		
		if (!emailInput && loginForm) {
			for (const input of textInputs) {
				const placeholder = (input.placeholder || '').toLowerCase();
				const id = (input.id || '').toLowerCase();
				if (placeholder === '...' && id.includes('login')) {
					emailInput = input;
					break;
				}
			}
		}
		
		const allButtons = searchRoot.querySelectorAll('button, input[type="submit"], [role="button"]');
		for (const btn of allButtons) {
			const text = (btn.textContent || btn.value || '').toLowerCase().trim();
			if (text === 'iniciar sesión' || text === 'iniciar') {
				submitButton = btn;
				break;
			}
		}
		
		if (!submitButton) {
			for (const btn of allButtons) {
				const btnType = (btn.type || '').toLowerCase();
				if (btnType === 'submit') {
					submitButton = btn;
					break;
				}
			}
		}
		
		if (emailInput) {
			emailInput.value = '%s';
			emailInput.dispatchEvent(new Event('input', { bubbles: true }));
			emailInput.dispatchEvent(new Event('change', { bubbles: true }));
		}
		
		if (passwordInput) {
			passwordInput.value = '%s';
			passwordInput.dispatchEvent(new Event('input', { bubbles: true }));
			passwordInput.dispatchEvent(new Event('change', { bubbles: true }));
		}
		
		if (submitButton) {
			submitButton.click();
		} else if (loginForm) {
			loginForm.submit();
		}
		
		return debug.join(' | ');
	})()`, cfg.Username, cfg.Password)

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(fillScript, &debugLog),
		chromedp.Sleep(4*time.Second),
		chromedp.Location(&currentURL),
		chromedp.Evaluate(`document.cookie.match(/JSESSIONID=([^;]+)/)?.[1] || ''`, &sessionID),
	)
	if err != nil {
		s.handleError(session, fmt.Errorf("login fill failed: %w", err))
		return
	}

	session.Progress.SetStage(models.StageNavigation)
	s.sendProgress(session, 50, models.StageNavigation, "Navegando a búsqueda...")

	err = chromedp.Run(browserCtx,
		chromedp.Navigate(searchURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(5*time.Second),
		chromedp.Location(&currentURL),
	)
	if err != nil {
		s.handleError(session, fmt.Errorf("search navigation failed: %w", err))
		return
	}

	session.Progress.SetStage(models.StageScraping)
	s.sendProgress(session, 70, models.StageScraping, "Extrayendo datos de hoteles...")

	hotelData := s.extractHotels(browserCtx, session)

	s.sendProgress(session, 90, models.StageProcessing, "Procesando resultados...")

	session.Results = append(session.Results, models.NewResult("hotel-1", hotelData))
	session.Status = models.SessionStatusCompleted
	session.Progress.SetProgress(100)
	session.Progress.SetStage(models.StageComplete)

	ws.SendToSession(session.ID, session.Progress)
	log.Printf("Session %s completed successfully", session.ID)
}

func (s *ScraperService) extractHotels(ctx context.Context, session *ScraperSession) map[string]interface{} {
	var result map[string]interface{}
	script := `(() => {
		try {
			const cookies = document.cookie;
			const url = window.location.href;
			const allText = document.body.innerText;
			
			const firstTotalMatch = allText.match(/Total: US\$[\d,.]+/);
			let finalPrice = null;
			if (firstTotalMatch) {
				const priceInLine = firstTotalMatch[0].match(/US\$[\d,.]+/);
				finalPrice = priceInLine ? priceInLine[0] : null;
			}
			
			const knownHotels = [
				'Radisson Blu Aruba', 'Radisson Blu', 'Radisson',
				'Hilton Aruba', 'Hilton',
				'Marriott',
				'Casa del Mar', 'Boardwalk',
				'ECLIPSE', 'Divi Aruba', 'Divi',
				'Renaissance Aruba', 'Embassy Suites'
			];
			
			let hotelName = null;
			for (const hotel of knownHotels) {
				if (allText.includes(hotel)) {
					hotelName = hotel;
					break;
				}
			}
			
			const allPriceMatches = allText.match(/US?\$[\d,.]+/g) || [];
			const cleanPrices = [...new Set(allPriceMatches)].filter(p => p.length > 2 && p.length < 15);
			
			return { 
				cookies: cookies,
				url: url,
				name: hotelName || 'Not found', 
				price: finalPrice || cleanPrices[0] || 'Not found'
			};
		} catch(e) {
			return { 
				cookies: '',
				name: 'Error: ' + e.message, 
				price: 'N/A'
			};
		}
	})()`

	chromedp.Run(ctx, chromedp.Evaluate(script, &result))
	return result
}

func (s *ScraperService) sendProgress(session *ScraperSession, percent float64, stage string, message string) {
	session.Progress.SetProgress(percent)
	session.Progress.SetStage(stage)
	session.Progress.Timestamp = time.Now()
	ws.SendToSession(session.ID, session.Progress)
}

func (s *ScraperService) handleError(session *ScraperSession, err error) {
	session.Error = err
	session.Status = models.SessionStatusFailed
	errMsg := err.Error()
	session.Progress.SetStage(models.StageComplete)
	ws.SendToSession(session.ID, session.Progress)
	log.Printf("Session %s failed: %v", session.ID, err)
	_ = errMsg
}

func (s *ScraperService) GetSession(sessionID string) (*ScraperSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[sessionID]
	return session, exists
}

func (s *ScraperService) GetStatus(sessionID string) (string, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[sessionID]
	if !exists {
		return "", 0
	}
	return session.Status, session.Progress.Progress
}

func (s *ScraperService) GetResults(sessionID string) []models.Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[sessionID]
	if !exists {
		return nil
	}
	return session.Results
}

func (s *ScraperService) CancelSession(sessionID string) error {
	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found")
	}

	session.Cancel()
	session.Status = models.SessionStatusCancelled
	return nil
}

func (s *ScraperService) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, session := range s.sessions {
		session.Cancel()
		session.Wg.Wait()
	}

	if s.pool != nil {
		s.pool.Close()
	}
}

var (
	debugMode = flag.Bool("debug", false, "Run in debug mode")
)

func init() {
	flag.Parse()
}
