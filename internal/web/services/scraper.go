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

	log.Printf("Session %s completed navigation to search", session.ID)

	time.Sleep(500 * time.Millisecond)

	session.Progress.SetStage(models.StageScraping)
	s.sendProgress(session, 70, models.StageScraping, "Extrayendo datos de hoteles...")

	time.Sleep(500 * time.Millisecond)

	hotelData := s.extractHotels(browserCtx, session)

	time.Sleep(500 * time.Millisecond)

	s.sendProgress(session, 90, models.StageProcessing, "Procesando resultados...")

	time.Sleep(500 * time.Millisecond)

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
			const title = document.title;
			
			// Find hotel cards - look for common patterns
			const cards = document.querySelectorAll('.hotel-card, .result-item, [class*="hotel"], .room-rate, [class*="card"]');
			
			const hotels = [];
			const seen = new Set();
			
			// First try specific hotel card selectors
			cards.forEach((card) => {
				// Try to find hotel name
				const nameEl = card.querySelector('h3, h4, h5, [class*="name"], [class*="title"], .hotel-name, .room-name');
				const priceEl = card.querySelector('.price, .total, [class*="price"], [class*="total"], [class*="amount"]');
				
				if (nameEl) {
					let name = nameEl.innerText.trim();
					let price = priceEl ? priceEl.innerText.trim() : '';
					
					// Clean price - extract just the number
					if (price) {
						const priceMatch = price.match(/US?\$[\d,]+/);
						if (priceMatch) {
							price = priceMatch[0];
						}
					}
					
					// Skip generic entries
					if (name && !name.startsWith('Hotel ') || price) {
						const key = name + '|' + price;
						if (!seen.has(key) && name.length > 2) {
							seen.add(key);
							hotels.push({ name, price: price || 'N/A' });
						}
					}
				}
			});
			
			// If we didn't find enough hotels, try extracting from text
			if (hotels.length < 5) {
				const knownHotels = [
					'Central Boutique Hotel', 'Victoria City Hotel', 'Hyatt Place',
					'Coconut Inn', 'Divi Village', 'Aruba Boutique', 'MVC Eagle Beach',
					'Radisson Blu', 'Renaissance', 'Amsterdam Manor', 'Holiday Inn',
					'Embassy Suites', 'Eagle Aruba', 'TRYP', 'Paradera Park',
					'Privada Stays', 'RH Boutique', 'Aruba\'s Life'
				];
				
				const priceRegex = /US?\$[\d,]+/g;
				const allPrices = [...new Set(allText.match(priceRegex) || [])].slice(0, 20);
				
				knownHotels.forEach(hotel => {
					if (allText.includes(hotel)) {
						// Find the closest price after the hotel name
						const hotelIndex = allText.indexOf(hotel);
						const textAfter = allText.substring(hotelIndex, hotelIndex + 200);
						const priceMatch = textAfter.match(/US?\$[\d,]+/);
						const price = priceMatch ? priceMatch[0] : 'N/A';
						
						const key = hotel + '|' + price;
						if (!seen.has(key)) {
							seen.add(key);
							hotels.push({ name: hotel, price });
						}
					}
				});
				
				// Add any prices we found if we don't have hotels yet
				if (hotels.length === 0) {
					allPrices.forEach((price, i) => {
						hotels.push({ name: 'Hotel ' + (i + 1), price });
					});
				}
			}
			
			// Sort by price
			hotels.sort((a, b) => {
				const aNum = parseFloat(a.price.replace(/[^\d]/g, '')) || 999999;
				const bNum = parseFloat(b.price.replace(/[^\d]/g, '')) || 999999;
				return aNum - bNum;
			});
			
			return { 
				cookies: cookies,
				url: url,
				title: title,
				hotels: hotels.slice(0, 50), // Limit to 50
				count: hotels.length
			};
		} catch(e) {
			return { error: e.message, url: window.location.href };
		}
	})()`

	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &result)); err != nil {
		log.Printf("Session %s: extractHotels error: %v", session.ID, err)
		return map[string]interface{}{"error": err.Error()}
	}

	log.Printf("Session %s: Extracted %d hotels", session.ID, result["count"])
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
