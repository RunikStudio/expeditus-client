package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"ExpeditusClient/internal/api"
	"ExpeditusClient/internal/browser"
	"ExpeditusClient/internal/config"
	"ExpeditusClient/internal/scrapers"
	delfosprov "ExpeditusClient/internal/scrapers/delfos"
	"ExpeditusClient/internal/web/models"
	"ExpeditusClient/internal/web/ws"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/cdproto/target"
)

const (
	hotelUrl       = "https://www.delfos.tur.ar/accommodation/00000/available/1?tripId=0"
	roomPrice      = ""
	defaultTimeout = 300 * time.Second

	// Legacy fallback values (used only when no order data is provided)
	defaultAccommodationValue = "Unknown Hotel (id: 00000)"
	defaultRoomType           = ""
	defaultMealPlan           = ""
)

type ScraperService struct {
	mu        sync.RWMutex
	sessions  map[string]*ScraperSession
	pool      *browser.Pool
	apiClient *api.Client
}

type ScraperSession struct {
	ID          string
	Order       *api.ScrapingOrder
	BookedPrice float64 // Price from CSV for comparison
	Ctx         context.Context
	Cancel      context.CancelFunc
	Wg          sync.WaitGroup
	Progress    models.ProgressUpdate
	Results     []models.Result
	Status      string
	Error       error
	StartTime   time.Time
	EndTime     *time.Time

	// Two-factor authentication coordination
	TwoFAInputSelector string         // CSS selector of the 2FA input (set when detected)
	TwoFASubmitSelector string        // CSS selector of the submit button (best effort)
	TwoFACode          chan string    // Receives the code provided by the user via the web UI
	TwoFAErr           chan error     // Reports whether the supplied code could be submitted
}

func NewScraperService() (*ScraperService, error) {
	ctx := context.Background()

	browserCfg := browser.DefaultConfig()
	browserCfg.Timeout = defaultTimeout
	browserCfg.Headless = false

	pool, err := browser.NewPool(ctx, browserCfg, 1)
	if err != nil {
		return nil, fmt.Errorf("creating browser pool: %w", err)
	}

	// Try to create API client
	apiClient, err := api.NewClient()
	if err != nil {
		log.Printf("Warning: API client not initialized: %v", err)
		apiClient = nil
	}

	svc := &ScraperService{
		sessions:  make(map[string]*ScraperSession),
		pool:      pool,
		apiClient: apiClient,
	}
	// Register this service as the legacy delfos runner so the delfos
	// provider can invoke the existing scraping methods by session id.
	legacyRunnerHolder = &serviceRunner{s: svc}
	return svc, nil
}

// init wires the delfos provider to call back into the legacy
// ScraperService methods. We use a process-wide holder because the
// scrapers.Registry resolves providers without a ScraperService
// reference.
func init() {
	delfosprov.Runner = legacyRunnerHolder
}

// legacyRunnerHolder is the most recently constructed ScraperService.
// It is updated by NewScraperService and consumed by the delfos
// provider. A single service is the expected runtime shape (one per
// process), so a package-level pointer is safe.
var legacyRunnerHolder delfosprov.LegacyRunner

// serviceRunner wires *ScraperService into the delfosprov.LegacyRunner
// interface so the delfos provider can invoke runScraping /
// runScrapingWithOrder from a separate goroutine.
type serviceRunner struct{ s *ScraperService }

// RunScrapingLegacy is invoked by the delfos provider when no order is
// supplied (legacy entry point).
func (r *serviceRunner) RunScrapingLegacy(ctx context.Context, sessionID string, cfg *config.LoginConfig) error {
	return r.s.runScrapingByID(sessionID, cfg)
}

// RunScrapingWithOrderLegacy is invoked by the delfos provider when an
// order is supplied. It is the bridge between the new provider
// abstraction and the existing 1700+ line runScrapingWithOrder function.
func (r *serviceRunner) RunScrapingWithOrderLegacy(ctx context.Context, sessionID string, cfg *config.LoginConfig, order *api.ScrapingOrder) error {
	return r.s.runScrapingWithOrderByID(sessionID, cfg, order)
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
		TwoFACode: make(chan string, 1),
		TwoFAErr:  make(chan error, 1),
	}

	session.Progress.SetStage(models.StageLogin)
	s.sessions[sessionID] = session

	// Without an order we always default to delfos since no other
	// provider has a sensible "no-order" entry point.
	go s.runScraping(session, cfg)

	return nil
}

// StartSessionWithOrder starts a scraping session with order data from API
func (s *ScraperService) StartSessionWithOrder(sessionID string, order *api.ScrapingOrder) error {
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
		ID:          sessionID,
		Order:       order,
		BookedPrice: order.BookedPrice, // Use price from CSV
		Ctx:         ctx,
		Cancel:      cancel,
		Status:      models.SessionStatusRunning,
		StartTime:   time.Now(),
		Progress:    *models.NewProgressUpdate(sessionID),
		TwoFACode:   make(chan string, 1),
		TwoFAErr:    make(chan error, 1),
	}

	session.Progress.SetStage(models.StageLogin)
	s.sessions[sessionID] = session

	// Dispatch to the registered provider when the order specifies a
	// non-delfos backend. Empty provider defaults to delfos so existing
	// callers stay compatible.
	providerName := scrapers.ProviderForOrder(order)
	if providerName != scrapers.ProviderDelfos {
		go s.dispatchToProvider(sessionID, order)
	} else {
		go s.runScrapingWithOrder(session, cfg, order)
	}

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

	if s.waitFor2FAIfNeeded(browserCtx, session) {
		return
	}

	session.Progress.SetStage(models.StageNavigation)
	s.sendProgress(session, 50, models.StageNavigation, "Navegando a búsqueda...")

	// Use order data if available, otherwise use default
	defaultSearchURL := "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&&departureDate=01/01/2030&arrivalDate=07/01/2030&hotelDestination=Destination::UNKNOWN"
	targetSearchURL := defaultSearchURL
	if session.Order != nil && session.Order.SearchURL != "" {
		targetSearchURL = session.Order.SearchURL
	}

	err = chromedp.Run(browserCtx,
		chromedp.Navigate(targetSearchURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Location(&currentURL),
	)
	if err != nil {
		s.handleError(session, fmt.Errorf("search navigation failed: %w", err))
		return
	}

	log.Printf("Session %s completed navigation to search", session.ID)

	// Wait for loading indicators to disappear, then check for completion
	var searchComplete bool
	for i := 0; i < 10; i++ {
		var loadingPresent bool
		chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				const loadingEl = document.querySelector('[class*="loading"], .spinner');
				return loadingEl !== null;
			})()`, &loadingPresent),
		)
		if loadingPresent {
			log.Printf("Session %s: Loading still present, waiting... (attempt %d/10)", session.ID, i+1)
			time.Sleep(1 * time.Second)
			continue
		}
		chromedp.Run(browserCtx,
			chromedp.Evaluate(`document.body.innerText.includes('Búsqueda completada')`, &searchComplete),
		)
		if searchComplete {
			log.Printf("Session %s: Search completed!", session.ID)
			break
		}
		log.Printf("Session %s: Waiting for search to complete... (attempt %d/10)", session.ID, i+1)
		time.Sleep(1 * time.Second)
	}

	time.Sleep(500 * time.Millisecond)

	// Wait for sidebar input to be enabled
	log.Printf("Session %s: Waiting for sidebar input to be enabled...", session.ID)
	var inputEnabled bool
	for i := 0; i < 10; i++ {
		err = chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				const inputs = document.querySelectorAll('input');
				for (const input of inputs) {
					const placeholder = (input.placeholder || '').toLowerCase();
					if (placeholder.includes('nombre del alojamiento')) {
						return !input.disabled;
					}
				}
				return false;
			})()`, &inputEnabled),
		)
		if err == nil && inputEnabled {
			log.Printf("Session %s: Sidebar input is now enabled!", session.ID)
			break
		}
		log.Printf("Session %s: Waiting for input... (attempt %d/30)", session.ID, i+1)
		time.Sleep(1 * time.Second)
	}

	if !inputEnabled {
		log.Printf("Session %s: WARNING - Sidebar input may still be disabled", session.ID)
	}

	session.Progress.SetStage(models.StageScraping)
	s.sendProgress(session, 75, models.StageScraping, "Buscando input de alojamiento...")

	fillAccommodationScript := fmt.Sprintf(`(() => {
		const inputs = document.querySelectorAll('input');
		let accommodationInput = null;
		
		for (const input of inputs) {
			const placeholder = (input.placeholder || '').toLowerCase();
			const ariaLabel = (input.getAttribute('aria-label') || '').toLowerCase();
			const name = (input.name || '').toLowerCase();
			
			if (placeholder.includes('nombre del alojamiento') || 
				ariaLabel.includes('nombre del alojamiento') ||
				name.includes('accommodation')) {
				accommodationInput = input;
				return 'Found input: ' + input.placeholder + ' | name: ' + input.name;
			}
		}
		
		for (const input of inputs) {
			const placeholder = (input.placeholder || '').toLowerCase();
			if (placeholder.includes('alojamiento') || placeholder.includes('hotel')) {
				accommodationInput = input;
				return 'Found partial match: ' + input.placeholder;
			}
		}
		
		return 'Input not found';
	})()`)

	var fillResult string
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(fillAccommodationScript, &fillResult),
	)
	if err != nil {
		log.Printf("Session %s: Error searching accommodation input: %v", session.ID, err)
	} else {
		log.Printf("Session %s: Accommodation input search: [%s]", session.ID, fillResult)
	}

	// Wait for input to be ready
	log.Printf("Session %s: Waiting for page to be ready...", session.ID)
	time.Sleep(1 * time.Second)

	// First, check if we're already in the search results page
	var pageState string
	chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			const text = document.body.innerText;
			if (text.includes('Búsqueda completada')) return 'SEARCH_COMPLETE';
			if (text.includes('Nombre del alojamiento')) return 'HAS_INPUT';
			return 'OTHER';
		})()`, &pageState),
	)
	log.Printf("Session %s: Page state: %s", session.ID, pageState)

	// Click on the sidebar input using coordinates (191, 599)
	inputX, inputY := 191, 599
	log.Printf("Session %s: CLICKING on input at coordinates (%d, %d)", session.ID, inputX, inputY)

	err = chromedp.Run(browserCtx,
		chromedp.MouseClickXY(float64(inputX), float64(inputY)),
		chromedp.Sleep(1*time.Second),
	)
	if err != nil {
		log.Printf("Session %s: Error clicking input: %v", session.ID, err)
	}

	// Verify click worked
	var isFocused string
	chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			const input = document.querySelector('input[placeholder*="alojamiento"]');
			return input ? (document.activeElement === input ? 'FOCUSED' : 'NOT_FOCUSED') : 'NOT_FOUND';
		})()`, &isFocused),
	)
	log.Printf("Session %s: Input focus state: %s", session.ID, isFocused)

	// Write hotel name to filter the list
	fillValueScript := fmt.Sprintf(`(() => {
		const inputs = document.querySelectorAll('input');
		
		const fullValue = '%s';
		// Extract just the hotel name (remove the " (id: XXX)" part)
		const searchName = fullValue.replace(/\s*\(id:\s*\d+\)/, '').trim();
		
		console.log('Searching for hotel: ' + searchName);
		
		for (const input of inputs) {
			const placeholder = (input.placeholder || '').toLowerCase();
			
			if (placeholder.includes('nombre del alojamiento')) {
				if (input.disabled) {
					return 'INPUT_DISABLED';
				}
				
				// Clear and type the hotel name
				input.value = searchName;
				
				// Trigger all relevant events
				input.dispatchEvent(new Event('input', { bubbles: true }));
				input.dispatchEvent(new Event('keyup', { bubbles: true }));
				input.dispatchEvent(new Event('change', { bubbles: true }));
				
				// Try to trigger filter if exists
				if (input.onkeyup) {
					input.onkeyup.call(input);
				}
				
				return 'TYPED: ' + searchName;
			}
		}
		
		return 'INPUT_NOT_FOUND';
	})()`, defaultAccommodationValue)

	var fillValueResult string
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(fillValueScript, &fillValueResult),
	)
	if err != nil {
		log.Printf("Session %s: Error filling: %v", session.ID, err)
	}

	log.Printf("Session %s: Fill result: [%s]", session.ID, fillValueResult)

	// Wait for list to update
	time.Sleep(1 * time.Second)

	// Get page debug after writing
	var pageDebugAfter string
	chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			const bodyText = document.body.innerText;
			const inputCount = document.querySelectorAll('input').length;
			// Find the input value
			let inputValue = '';
			for (const inp of document.querySelectorAll('input')) {
				if ((inp.placeholder || '').toLowerCase().includes('alojamiento')) {
					inputValue = inp.value;
					break;
				}
			}
			return 'inputs:' + inputCount + '|input_value:' + inputValue + '|text:' + bodyText.substring(0, 300);
		})()`, &pageDebugAfter),
	)
	log.Printf("Session %s: Page debug after fill: [%s]", session.ID, pageDebugAfter)

	// Debug: show current input value
	var inputValue string
	chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			const inputs = document.querySelectorAll('input');
			for (const input of inputs) {
				if ((input.placeholder || '').toLowerCase().includes('nombre del alojamiento')) {
					return input.value;
				}
			}
			return 'INPUT_NOT_FOUND';
		})()`, &inputValue),
	)
	log.Printf("Session %s: Input value after typing: [%s]", session.ID, inputValue)

	// Wait for the hotel list to filter
	time.Sleep(1 * time.Second)

	// Click on the first hotel in the list
	clickHotelScript := `(() => {
		console.log('Looking for first hotel link...');
		
		// Find all links in the page that could be hotel entries
		const allLinks = document.querySelectorAll('a');
		let hotelLink = null;
		
		for (const link of allLinks) {
			const text = (link.textContent || '').trim().toLowerCase();
			const href = (link.href || '').toLowerCase();
			// Look for links with "ver opciones", "ver hotel", or href containing hotel/extended
			if ((text.includes('ver opciones') || text.includes('ver hotel') || href.includes('extended') || href.includes('hotel')) && 
				!text.includes('entrar') && !text.includes('login') && !text.includes('cerrar')) {
				hotelLink = link;
				console.log('Found hotel link: ' + text);
				break;
			}
		}
		
		if (hotelLink) {
			const href = hotelLink.href || hotelLink.getAttribute('data-url');
			if (href) {
				window.location.href = href;
				return 'CLICKED: ' + href;
			}
		}
		
		// Fallback: click the first substantial link
		for (const link of allLinks) {
			const text = (link.textContent || '').trim();
			const href = link.href || '';
			if (text && text.length > 3 && href && href.length > 30 && 
				!text.toLowerCase().includes('entrar') && 
				!text.toLowerCase().includes('login')) {
				window.location.href = href;
				return 'FALLBACK_CLICKED: ' + text;
			}
		}
		
		return 'NO_LINKS_FOUND';
	})()`

	var clickResult string
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(clickHotelScript, &clickResult),
	)

	log.Printf("Session %s: Click result: [%s]", session.ID, clickResult)

	log.Printf("Session %s: Waiting for rooms to load...", session.ID)
	for i := 0; i < 10; i++ {
		var roomsLoaded map[string]interface{}
		err = chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				const text = document.body.innerText;
				const loading = text.includes('Estamos buscando los mejores precios');
				const ready = text.includes('Opciones de reserva');
				return { loading, ready };
			})()`, &roomsLoaded),
		)
		if err == nil && roomsLoaded != nil {
			if ready, ok := roomsLoaded["ready"].(bool); ok && ready {
				log.Printf("Session %s: Rooms loaded successfully!", session.ID)
				break
			}
		}
		log.Printf("Session %s: Waiting for rooms... (attempt %d/10)", session.ID, i+1)
		time.Sleep(1 * time.Second)
	}

	var hotelPageCheck string
	checkHotelScript := fmt.Sprintf(`(() => {
		const expectedName = '%s'.replace(/\\s*\\(id:\\s*\\d+\\)/, '').trim().toLowerCase();
		
		const url = window.location.href;
		const bodyText = document.body.innerText;
		
		const enResultados = bodyText.includes('Búsqueda completada');
		
		const nameEl = document.querySelector('.dev-hotel-title-name') || 
		               document.querySelector('.c-extended__title') ||
		               document.querySelector('.hotel-name.dev-hotel-title') ||
		               document.querySelector('[class*="hotel-title"]');
		
		let foundName = '';
		if (nameEl) {
			foundName = nameEl.innerText.trim().toLowerCase();
		}
		
		if (!foundName) {
			const hotelIdEl = document.getElementById('openHotel2');
			if (hotelIdEl) {
				foundName = hotelIdEl.innerText.trim().toLowerCase();
			}
		}
		
		const opcionesReserva = bodyText.includes('Opciones de reserva');
		
		if (enResultados) {
			return 'STILL IN RESULTS - searching for hotel: ' + foundName;
		}
		
		if (foundName.includes(expectedName) || expectedName.includes(foundName)) {
			return 'URL: ' + url + ' | HOTEL: ' + foundName + ' | Opciones: ' + opcionesReserva;
		}
		
		return 'UNKNOWN_PAGE - url: ' + url;
	})()`, defaultAccommodationValue)

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(checkHotelScript, &hotelPageCheck),
	)
	if err != nil {
		log.Printf("Session %s: Error checking hotel: %v", session.ID, err)
	}

	log.Printf("Session %s: Hotel page check: [%s]", session.ID, hotelPageCheck)

	time.Sleep(1 * time.Second)

	var roomsButtonResult string
	roomsButtonScript := `(() => {
		const allIds = [];
		document.querySelectorAll('[id]').forEach(el => {
			const id = el.id;
			if (id.toLowerCase().includes('accommodation') || 
				id.toLowerCase().includes('section') ||
				id.toLowerCase().includes('mobile') ||
				id.toLowerCase().includes('habitacion')) {
				allIds.push(id);
			}
		});
		
		const sectionPanel = document.getElementById('accommodationSectionPanel');
		
		if (sectionPanel) {
			const buttons = sectionPanel.querySelectorAll('button, a');
			for (const btn of buttons) {
				const text = (btn.textContent || '').toLowerCase().trim();
				if (text.includes('habitaciones')) {
					btn.click();
					return 'CLICKED: HABITACIONES (section panel)';
				}
			}
		}
		
		const mobileNav = document.getElementById('accommodation-detail:c-mobile-navigation');
		
		if (mobileNav) {
			const buttons = mobileNav.querySelectorAll('button, a');
			for (const btn of buttons) {
				const text = (btn.textContent || '').toLowerCase().trim();
				if (text.includes('ver habitaciones')) {
					btn.click();
					return 'CLICKED: Ver habitaciones (mobile nav)';
				}
			}
		}
		
		const allButtons = document.querySelectorAll('button, a');
		for (const btn of allButtons) {
			const text = (btn.textContent || '').toLowerCase().trim();
			if (text.includes('habitaciones')) {
				btn.click();
				return 'CLICKED: Habitaciones (fallback)';
			}
		}
		
		return 'NO CLICK - IDs found: ' + JSON.stringify(allIds.slice(0, 10));
	})()`

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(roomsButtonScript, &roomsButtonResult),
	)
	if err == nil {
		log.Printf("Session %s: Rooms button: [%s]", session.ID, roomsButtonResult)
	}

	time.Sleep(1 * time.Second)

	hotelData := s.extractCurrentHotel(browserCtx, session)

	time.Sleep(500 * time.Millisecond)

	s.sendProgress(session, 90, models.StageProcessing, "Procesando resultados...")

	time.Sleep(500 * time.Millisecond)

	// Get result ID safely
	resultID := ""
	if session.Order != nil {
		resultID = session.Order.ID
	}
	if resultID == "" {
		resultID = session.ID
	}

	result := models.NewResult(resultID, hotelData)
	if screenshot, ok := hotelData["screenshot"].(string); ok && screenshot != "" {
		result.Screenshot = screenshot
	}
	session.Results = append(session.Results, result)
	session.Status = models.SessionStatusCompleted
	session.Progress.SetProgress(100)
	session.Progress.SetStage(models.StageComplete)

	ws.SendToSession(session.ID, session.Progress)
	log.Printf("Session %s completed successfully", session.ID)
}

// clickSendCodeButtonIfPresent looks for the "Enviar código" / "Send code"
// button that some 2FA popups display before showing the actual verification
// input. When found, clicks it and returns true.
func (s *ScraperService) clickSendCodeButtonIfPresent(ctx context.Context, session *ScraperSession) bool {
	findScript := `(() => {
		const keywords = ['enviar código', 'enviar codigo', 'enviar código', 'send code', 'send verification', 'reenviar código', 'reenviar codigo'];
		const candidates = Array.from(document.querySelectorAll('button, a, input[type="button"], input[type="submit"], [role="button"]'));

		// Only consider elements currently visible.
		const visible = candidates.filter(el => el.offsetParent !== null);

		// First, look for an exact-ish text match in the element itself.
		for (const el of visible) {
			const text = (el.innerText || el.textContent || el.value || '').trim().toLowerCase();
			if (!text) continue;
			if (keywords.some(k => text === k || text.includes(k))) {
				let selector = '';
				if (el.id) selector = '#' + CSS.escape(el.id);
				else selector = 'temp-' + Math.random().toString(36).slice(2);
				return { found: true, selector: selector, text: text };
			}
		}

		return { found: false };
	})()`

	type findResult struct {
		Found    bool   `json:"found"`
		Selector string `json:"selector"`
		Text     string `json:"text"`
	}

	var found findResult
	probeCtx, cancelProbe := context.WithTimeout(ctx, 10*time.Second)
	err := chromedp.Run(probeCtx, chromedp.Evaluate(findScript, &found))
	cancelProbe()
	if err != nil {
		log.Printf("Session %s: send-code probe error: %v", session.ID, err)
		return false
	}

	if !found.Found {
		return false
	}

	log.Printf("Session %s: detected 'enviar código' button (%q), clicking it...", session.ID, found.Text)
	session.Progress.SetStage(models.StageTwoFA)
	session.Progress.SetCurrentAction("Solicitando código de verificación...")
	session.Progress.SetProgress(32)
	ws.SendToSession(session.ID, session.Progress)

	// If we don't have an ID-based selector, we need to find the element by
	// text content at click-time because we can't synthesize a CSS selector
	// for "random text". Fall back to clicking by text.
	clickScript := `(() => {
		const keywords = ['enviar código', 'enviar codigo', 'send code', 'send verification', 'reenviar código', 'reenviar codigo'];
		const candidates = Array.from(document.querySelectorAll('button, a, input[type="button"], input[type="submit"], [role="button"]'));
		for (const el of candidates) {
			if (!el.offsetParent) continue;
			const text = (el.innerText || el.textContent || el.value || '').trim().toLowerCase();
			if (keywords.some(k => text === k || text.includes(k))) {
				el.click();
				return 'clicked:' + text;
			}
		}
		return 'not-found';
	})()`

	var clickRes string
	clickCtx, cancelClick := context.WithTimeout(ctx, 10*time.Second)
	err = chromedp.Run(clickCtx, chromedp.Evaluate(clickScript, &clickRes))
	cancelClick()
	if err != nil {
		log.Printf("Session %s: send-code click error: %v", session.ID, err)
		return false
	}
	log.Printf("Session %s: send-code click result: %s", session.ID, clickRes)
	return clickRes != "" && clickRes != "not-found"
}

// waitFor2FAIfNeeded detects a two-factor authentication prompt on the page and,
// when present, pauses the scraping goroutine until the user submits a 2FA code
// through the web UI. Returns true when the function handled the 2FA flow
// (caller should stop further navigation), false when no 2FA was required.
//
// Flow:
//  1. Some sites (e.g. Delfos Tour) show a popup with a button labelled
//     "Enviar código" / "Send code" that has to be clicked before the actual
//     verification input appears. This helper detects that button first and
//     clicks it, then polls for the actual input.
//  2. Once the verification input is on the page, the helper parks the session
//     in waiting_2fa and waits for the user to POST a code through
//     /api/client/scrape/session/:id/2fa. The code is typed into the input and
//     the closest submit button is clicked.
func (s *ScraperService) waitFor2FAIfNeeded(ctx context.Context, session *ScraperSession) bool {
	// Phase 1: click the "enviar código" button if the popup shows one. This
	// signals that the site is in a 2FA flow, even before the input renders.
	twoFAFlowStarted := s.clickSendCodeButtonIfPresent(ctx, session)

	if twoFAFlowStarted {
		// Phase 2: poll for the actual code input to appear after the email
		// is sent. We give it up to 15 seconds before giving up.
		if !s.waitFor2FAInputToAppear(ctx, session, 15*time.Second) {
			log.Printf("Session %s: 2FA input did not appear within timeout", session.ID)
			return false
		}
	} else {
		// No "enviar código" button. Maybe the page already shows the 2FA
		// input directly, or maybe there is no 2FA at all. Run the detection
		// once; if nothing is found, return false.
		detectScript := `(() => {
			const keywords = ['código', 'codigo', 'verification', 'verify', '2fa', 'two-factor', 'two factor', 'otp', 'autenticación', 'autenticacion'];
			const inputs = Array.from(document.querySelectorAll('input'));
			for (const input of inputs) {
				if (!input.offsetParent && input.type !== 'text') continue;
				const type = (input.type || '').toLowerCase();
				if (type === 'hidden' || type === 'password' || type === 'email' || type === 'submit' || type === 'button') continue;
				const maxLen = input.maxLength > 0 ? input.maxLength : 12;
				if (maxLen > 12) continue;
				const id = (input.id || '').toLowerCase();
				const name = (input.name || '').toLowerCase();
				const placeholder = (input.placeholder || '').toLowerCase();
				const aria = (input.getAttribute('aria-label') || '').toLowerCase();
				const autocomp = (input.getAttribute('autocomplete') || '').toLowerCase();
				const inputmode = (input.getAttribute('inputmode') || '').toLowerCase();
				const pattern = (input.getAttribute('pattern') || '').toLowerCase();
				const matchesKeyword = keywords.some(k => id.includes(k) || name.includes(k) || placeholder.includes(k) || aria.includes(k));
				const digitOnly = (inputmode === 'numeric' || inputmode === 'decimal' || /\d/.test(pattern) || maxLen <= 8);
				const oneTimeCode = autocomp === 'one-time-code';
				if (matchesKeyword || (oneTimeCode && digitOnly) || (digitOnly && (maxLen <= 8) && (placeholder.includes('código') || placeholder.includes('codigo') || placeholder.includes('code') || placeholder.includes('verify') || placeholder.includes('verif')))) {
					return { detected: true, maxLength: maxLen };
				}
			}
			return { detected: false };
		})()`
		type r struct {
			Detected  bool `json:"detected"`
			MaxLength int  `json:"maxLength"`
		}
		var res r
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := chromedp.Run(probeCtx, chromedp.Evaluate(detectScript, &res))
		cancel()
		if err != nil || !res.Detected {
			return false
		}
		// Park the session immediately.
		s.parkFor2FA(ctx, session, "", "", res.MaxLength)
		return true
	}

	// We already clicked "enviar código"; the input is now on the page.
	// Capture its selector and park the session.
	selector, submitSelector, maxLen := s.detect2FAInputSelector(ctx)
	s.parkFor2FA(ctx, session, selector, submitSelector, maxLen)
	return true
}

// parkFor2FA marks the session as waiting_2fa and blocks until the user
// submits a code (or the session is cancelled).
func (s *ScraperService) parkFor2FA(ctx context.Context, session *ScraperSession, inputSel, submitSel string, maxLen int) {
	session.TwoFAInputSelector = inputSel
	session.TwoFASubmitSelector = submitSel
	session.Status = models.SessionStatusWaiting2FA
	session.Progress.SetStage(models.StageTwoFA)
	session.Progress.SetCurrentAction("Esperando código de verificación (2FA)...")
	session.Progress.SetStatus(models.SessionStatusWaiting2FA)
	session.Progress.SetProgress(35)
	ws.SendToSession(session.ID, session.Progress)

	log.Printf("Session %s: parked in waiting_2fa (inputSel=%q, submitSel=%q, maxLen=%d)", session.ID, inputSel, submitSel, maxLen)

	select {
	case <-session.Ctx.Done():
		log.Printf("Session %s cancelled while waiting for 2FA", session.ID)
		return
	case code := <-session.TwoFACode:
		log.Printf("Session %s: received 2FA code from user (%d chars)", session.ID, len(code))
		s.continueAfter2FA(ctx, session, code)
	}
}

// waitFor2FAInputToAppear polls the page until a 2FA input appears or the
// timeout elapses. Returns true when the input was found.
func (s *ScraperService) waitFor2FAInputToAppear(ctx context.Context, session *ScraperSession, timeout time.Duration) bool {
	detectScript := `(() => {
			const keywords = ['código', 'codigo', 'verification', 'verify', '2fa', 'two-factor', 'two factor', 'otp', 'autenticación', 'autenticacion'];
			const inputs = Array.from(document.querySelectorAll('input'));
			for (const input of inputs) {
				if (!input.offsetParent && input.type !== 'text') continue;
				const type = (input.type || '').toLowerCase();
				if (type === 'hidden' || type === 'password' || type === 'email' || type === 'submit' || type === 'button') continue;
				const maxLen = input.maxLength > 0 ? input.maxLength : 12;
				if (maxLen > 12) continue;
				const id = (input.id || '').toLowerCase();
				const name = (input.name || '').toLowerCase();
				const placeholder = (input.placeholder || '').toLowerCase();
				const aria = (input.getAttribute('aria-label') || '').toLowerCase();
				const autocomp = (input.getAttribute('autocomplete') || '').toLowerCase();
				const inputmode = (input.getAttribute('inputmode') || '').toLowerCase();
				const pattern = (input.getAttribute('pattern') || '').toLowerCase();
				const matchesKeyword = keywords.some(k => id.includes(k) || name.includes(k) || placeholder.includes(k) || aria.includes(k));
				const digitOnly = (inputmode === 'numeric' || inputmode === 'decimal' || /\d/.test(pattern) || maxLen <= 8);
				const oneTimeCode = autocomp === 'one-time-code';
				if (matchesKeyword || (oneTimeCode && digitOnly) || (digitOnly && (maxLen <= 8) && (placeholder.includes('código') || placeholder.includes('codigo') || placeholder.includes('code') || placeholder.includes('verify') || placeholder.includes('verif')))) {
					let selector = '';
					if (input.id) selector = '#' + CSS.escape(input.id);
					else if (input.name) selector = 'input[name="' + input.name + '"]';
					let submitSelector = '';
					const form = input.closest('form');
					if (form) {
						const submit = form.querySelector('button[type="submit"], input[type="submit"], button:not([type])');
						if (submit && submit.id) submitSelector = '#' + CSS.escape(submit.id);
					}
					return JSON.stringify({
						detected: true,
						selector: selector,
						submitSelector: submitSelector,
						maxLength: maxLen
					});
				}
			}
			return JSON.stringify({detected: false});
		})()`

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var raw string
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := chromedp.Run(probeCtx, chromedp.Evaluate(detectScript, &raw))
		cancel()
		if err == nil {
			var res struct {
				Detected       bool   `json:"detected"`
				Selector       string `json:"selector"`
				SubmitSelector string `json:"submitSelector"`
				MaxLength      int    `json:"maxLength"`
			}
			if err := json.Unmarshal([]byte(raw), &res); err == nil && res.Detected {
				session.TwoFAInputSelector = res.Selector
				session.TwoFASubmitSelector = res.SubmitSelector
				log.Printf("Session %s: 2FA input appeared (selector=%q)", session.ID, res.Selector)
				return true
			}
		}
		select {
		case <-session.Ctx.Done():
			return false
		case <-time.After(1 * time.Second):
		}
	}
	return false
}

// detect2FAInputSelector runs the detection script once (assumes the input is
// already on the page) and returns its selector plus the closest submit button.
func (s *ScraperService) detect2FAInputSelector(ctx context.Context) (string, string, int) {
	script := `(() => {
			const keywords = ['código', 'codigo', 'verification', 'verify', '2fa', 'two-factor', 'two factor', 'otp', 'autenticación', 'autenticacion'];
			const inputs = Array.from(document.querySelectorAll('input'));
			for (const input of inputs) {
				if (!input.offsetParent && input.type !== 'text') continue;
				const type = (input.type || '').toLowerCase();
				if (type === 'hidden' || type === 'password' || type === 'email' || type === 'submit' || type === 'button') continue;
				const maxLen = input.maxLength > 0 ? input.maxLength : 12;
				if (maxLen > 12) continue;
				const id = (input.id || '').toLowerCase();
				const name = (input.name || '').toLowerCase();
				const placeholder = (input.placeholder || '').toLowerCase();
				const aria = (input.getAttribute('aria-label') || '').toLowerCase();
				const autocomp = (input.getAttribute('autocomplete') || '').toLowerCase();
				const inputmode = (input.getAttribute('inputmode') || '').toLowerCase();
				const pattern = (input.getAttribute('pattern') || '').toLowerCase();
				const matchesKeyword = keywords.some(k => id.includes(k) || name.includes(k) || placeholder.includes(k) || aria.includes(k));
				const digitOnly = (inputmode === 'numeric' || inputmode === 'decimal' || /\d/.test(pattern) || maxLen <= 8);
				const oneTimeCode = autocomp === 'one-time-code';
				if (matchesKeyword || (oneTimeCode && digitOnly) || (digitOnly && (maxLen <= 8) && (placeholder.includes('código') || placeholder.includes('codigo') || placeholder.includes('code') || placeholder.includes('verify') || placeholder.includes('verif')))) {
					let selector = '';
					if (input.id) selector = '#' + CSS.escape(input.id);
					else if (input.name) selector = 'input[name="' + input.name + '"]';
					let submitSelector = '';
					const form = input.closest('form');
					if (form) {
						const submit = form.querySelector('button[type="submit"], input[type="submit"], button:not([type])');
						if (submit && submit.id) submitSelector = '#' + CSS.escape(submit.id);
					}
					return JSON.stringify({selector: selector, submitSelector: submitSelector, maxLength: maxLen});
				}
			}
			return JSON.stringify({selector: '', submitSelector: '', maxLength: 0});
		})()`

	var raw string
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := chromedp.Run(probeCtx, chromedp.Evaluate(script, &raw))
	cancel()
	if err != nil {
		return "", "", 0
	}
	var res struct {
		Selector       string `json:"selector"`
		SubmitSelector string `json:"submitSelector"`
		MaxLength      int    `json:"maxLength"`
	}
	_ = json.Unmarshal([]byte(raw), &res)
	return res.Selector, res.SubmitSelector, res.MaxLength
}

// continueAfter2FA fills the parked 2FA input with the user-supplied code
// and clicks the most likely submit button. Resumes the session afterwards.
func (s *ScraperService) continueAfter2FA(ctx context.Context, session *ScraperSession, code string) {
	fillScript := fmt.Sprintf(`(() => {
			const input = %s;
			if (!input) return 'input-missing';
			input.focus();
			input.value = '%s';
			input.dispatchEvent(new Event('input', { bubbles: true }));
			input.dispatchEvent(new Event('change', { bubbles: true }));
			return 'filled';
		})()`, jsSelectorExpr(session.TwoFAInputSelector), code)

	var fillRes string
	fillCtx, cancelFill := context.WithTimeout(ctx, 10*time.Second)
	err := chromedp.Run(fillCtx, chromedp.Evaluate(fillScript, &fillRes))
	cancelFill()
	if err != nil || fillRes != "filled" {
		errMsg := fmt.Errorf("failed to fill 2FA input: %v (res=%s)", err, fillRes)
		session.Progress.SetCurrentAction("Error al ingresar código 2FA")
		ws.SendToSession(session.ID, session.Progress)
		select {
		case session.TwoFAErr <- errMsg:
		default:
		}
		s.handleError(session, errMsg)
		return
	}

	submitScript := fmt.Sprintf(`(() => {
			const input = %s;
			if (!input) return 'input-missing';
			const form = input.closest('form');
			const tryClick = (sel) => {
				if (!sel) return false;
				try {
					const el = document.querySelector(sel);
					if (el) { el.click(); return true; }
				} catch (e) {}
				return false;
			};
			if (tryClick(%s)) return 'clicked-selector';
			if (form) {
				const btn = form.querySelector('button[type="submit"], input[type="submit"], button:not([type])');
				if (btn) { btn.click(); return 'clicked-form-button'; }
			}
			const enterDown = new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true});
			const enterUp = new KeyboardEvent('keyup', {key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true});
			input.dispatchEvent(enterDown);
			input.dispatchEvent(enterUp);
			return 'pressed-enter';
		})()`, jsSelectorExpr(session.TwoFAInputSelector), jsSelectorExpr(session.TwoFASubmitSelector))

	var submitRes string
	submitCtx, cancelSubmit := context.WithTimeout(ctx, 10*time.Second)
	err = chromedp.Run(submitCtx, chromedp.Evaluate(submitScript, &submitRes))
	cancelSubmit()
	if err != nil {
		log.Printf("Session %s: 2FA submit error: %v (res=%s)", session.ID, err, submitRes)
	} else {
		log.Printf("Session %s: 2FA submit result: %s", session.ID, submitRes)
	}

	session.Progress.SetStage(models.StageLogin)
	session.Progress.SetCurrentAction("Código 2FA enviado, continuando...")
	session.Progress.SetProgress(40)
	session.Status = models.SessionStatusRunning
	ws.SendToSession(session.ID, session.Progress)

	time.Sleep(2 * time.Second)
}


// jsSelectorExpr converts a CSS selector into a JS expression that can be
// embedded inside an Immediately-Invoked Function Expression template literal.
func jsSelectorExpr(selector string) string {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "null"
	}
	// JSON-encode the string so quotes/backslashes/newlines are properly escaped.
	b, _ := json.Marshal(selector)
	return fmt.Sprintf("document.querySelector(%s)", string(b))
}

// checkDatePopup checks if a date validation modal dialog appeared
func (s *ScraperService) checkDatePopup(ctx context.Context) bool {
	var hasPopup bool
	script := `(() => {
		// 1. Verify backdrop exists (indicates real modal)
		const backdrop = document.querySelector('.modal-backdrop, [class*="backdrop"], .overlay');
		if (!backdrop) {
			return false;
		}

		// 2. Only modal-dialogs, not toasts
		const modal = document.querySelector('.modal-dialog, .modal-content, [role="dialog"][aria-modal="true"]');
		if (!modal) {
			return false;
		}

		const style = window.getComputedStyle(modal);
		if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') {
			return false;
		}

		// 3. Text must be substantial (not a 5 char toast)
		const text = (modal.innerText || modal.textContent || '').trim();
		if (text.length < 50) {
			return false;
		}

		// 4. SPECIFIC patterns for date validation errors only
		const patterns = [
			'fecha inválida', 'fecha no válida', 'fecha incorrecta',
			'fecha pasada', 'rango de fechas', 'formato de fecha'
		];

		return patterns.some(p => text.toLowerCase().includes(p));
	})()`

	chromedp.Run(ctx, chromedp.Evaluate(script, &hasPopup))
	return hasPopup
}

// waitForNewWindow waits for a new browser target (window/tab) to appear
func (s *ScraperService) waitForNewWindow(ctx context.Context) error {
	timeout := 10 * time.Second
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var windowCount int
		chromedp.Run(ctx,
			chromedp.Evaluate(`window.length`, &windowCount),
		)
		if windowCount > 1 {
			log.Printf("Multiple windows detected: %d", windowCount)
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for new window")
}

// switchToNewWindow switches chromedp context to a new popup window if one exists.
// It uses chromedp.ListenTarget to detect when a new target (popup) is created and
// creates a new context bound to that target.
func (s *ScraperService) switchToNewWindow(ctx context.Context, browserCtx context.Context) (context.Context, error) {
	targetCreated := make(chan string)

	listenCtx, cancelListen := chromedp.NewContext(browserCtx)
	if listenCtx == nil {
		return ctx, fmt.Errorf("failed to create listener context")
	}

	chromedp.ListenTarget(listenCtx, func(ev interface{}) {
		if ev, ok := ev.(*target.EventTargetCreated); ok && ev.TargetInfo != nil {
			select {
			case targetCreated <- string(ev.TargetInfo.TargetID):
			default:
			}
		}
	})
	defer cancelListen()

	select {
	case targetID := <-targetCreated:
		log.Printf("Popup window detected with target ID: %s", targetID)
		newCtx, newCancel := chromedp.NewContext(listenCtx, chromedp.WithTargetID(target.ID(targetID)))
		if newCtx == nil {
			return ctx, fmt.Errorf("failed to create context for popup")
		}
		_ = newCancel
		return newCtx, nil
	case <-time.After(3 * time.Second):
		return ctx, fmt.Errorf("timeout waiting for popup window")
	}
}

// runScrapingWithOrder runs the scraping using order data from the API
func (s *ScraperService) runScrapingWithOrder(session *ScraperSession, cfg *config.LoginConfig, order *api.ScrapingOrder) {
	log.Printf("Session %s: runScrapingWithOrder STARTED", session.ID)
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

	// Get values from order or use defaults
	searchURL := order.SearchURL
	hotelName := order.HotelName
	hotelID := order.HotelID
	roomType := order.RoomType
	mealPlan := order.MealPlan

	if searchURL == "" {
		log.Printf("Session %s: No order provided or empty SearchURL, using defaults", session.ID)
		// Use generic fallback values - this should not happen if API is working correctly
		hotelName = "Generic Hotel"
		hotelID = "00000"
		searchURL = "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&&departureDate=01/01/2027&arrivalDate=07/01/2027&hotelDestination=Destination::UNKNOWN"
		roomType = defaultRoomType
		mealPlan = defaultMealPlan
	}

	defaultAccommodationValue := fmt.Sprintf("%s (id: %s)", hotelName, hotelID)

	log.Printf("Session %s: defaultAccommodationValue = '%s'", session.ID, defaultAccommodationValue)
	log.Printf("Session %s: Starting scraping for order: %s", session.ID, order.ServiceRef)
	log.Printf("Session %s: SearchURL: %s", session.ID, searchURL)
	log.Printf("Session %s: Hotel: %s, Room: %s, MealPlan: %s", session.ID, hotelName, roomType, mealPlan)

	s.sendProgress(session, 5, models.StageLogin, "Iniciando navegador...")

	browserCtx, cancel := s.pool.NewContext(session.Ctx)
	defer cancel()

	session.Progress.SetStage(models.StageLogin)
	s.sendProgress(session, 10, models.StageLogin, "Navegando a Delfos...")

	var sessionID, currentURL string

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

	var passwordEnabled bool
	for i := 0; i < 10; i++ {
		chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				const pwd = document.querySelector('input[type="password"]');
				return pwd && !pwd.disabled;
			})()`, &passwordEnabled),
		)
		if passwordEnabled {
			log.Printf("Session %s: Password field enabled", session.ID)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	fillScript := fmt.Sprintf(`(() => {
		const emailInput = document.querySelector('input[type="text"]');
		const passwordInput = document.querySelector('input[type="password"]');
		
		if (!emailInput || !passwordInput) {
			return 'inputs_not_found';
		}
		
		emailInput.focus();
		emailInput.value = '%s';
		emailInput.dispatchEvent(new Event('focus', { bubbles: true }));
		emailInput.dispatchEvent(new Event('input', { bubbles: true }));
		emailInput.dispatchEvent(new Event('change', { bubbles: true, cancelable: true }));
		
		passwordInput.focus();
		passwordInput.value = '%s';
		passwordInput.dispatchEvent(new Event('focus', { bubbles: true }));
		passwordInput.dispatchEvent(new Event('input', { bubbles: true }));
		passwordInput.dispatchEvent(new Event('change', { bubbles: true, cancelable: true }));
		
		// Find and click the submit button instead of pressing Enter
		const form = emailInput.closest('form');
		if (form) {
			const submitBtn = form.querySelector('button[type="submit"], input[type="submit"]');
			if (submitBtn) {
				submitBtn.click();
				return 'submitted_via_button|' + (emailInput?.id || 'unknown') + '|' + (passwordInput?.id || 'unknown');
			}
		}
		
		// Fallback to Enter key
		const enterDown = new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true});
		const enterUp = new KeyboardEvent('keyup', {key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true});
		passwordInput.dispatchEvent(enterDown);
		passwordInput.dispatchEvent(enterUp);
		
		return 'submitted_via_enter|' + (emailInput?.id || 'unknown') + '|' + (passwordInput?.id || 'unknown');
	})()`, cfg.Username, cfg.Password)

	var fillResult string
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(fillScript, &fillResult),
		chromedp.Sleep(4*time.Second),
		chromedp.Location(&currentURL),
		chromedp.Evaluate(`document.cookie.match(/JSESSIONID=([^;]+)/)?.[1] || ''`, &sessionID),
	)
	if err != nil {
		s.handleError(session, fmt.Errorf("login fill failed: %w", err))
		return
	}

	if s.waitFor2FAIfNeeded(browserCtx, session) {
		return
	}

	session.Progress.SetStage(models.StageNavigation)
	s.sendProgress(session, 50, models.StageNavigation, "Navegando a búsqueda...")

	defaultSearchURL := "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&&departureDate=01/01/2030&arrivalDate=07/01/2030&hotelDestination=Destination::UNKNOWN"
	targetSearchURL := defaultSearchURL
	if session.Order != nil && session.Order.SearchURL != "" {
		targetSearchURL = session.Order.SearchURL
	}

	log.Printf("Session %s: Setting up popup detection before navigation...", session.ID)

	targetCreated := make(chan string)
	listenCtx, cancelListen := chromedp.NewContext(browserCtx)
	if listenCtx == nil {
		log.Printf("Session %s: Failed to create listener context", session.ID)
	} else {
		chromedp.ListenTarget(listenCtx, func(ev interface{}) {
			if ev, ok := ev.(*target.EventTargetCreated); ok && ev.TargetInfo != nil {
				select {
				case targetCreated <- string(ev.TargetInfo.TargetID):
				default:
				}
			}
		})
	}

	log.Printf("Session %s: Navigating to search URL (popup should open)...", session.ID)
	err = chromedp.Run(listenCtx,
		chromedp.Navigate(targetSearchURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Location(&currentURL),
	)
	if err != nil {
		cancelListen()
		s.handleError(session, fmt.Errorf("search navigation failed: %w", err))
		return
	}

	log.Printf("Session %s: URL after navigation: %s", session.ID, currentURL)

	// Check if we're on a login page (redirect happened)
	if strings.Contains(currentURL, "login") || strings.Contains(currentURL, "Login") {
		log.Printf("Session %s: ERROR - Redirected to login page after search navigation!", session.ID)
		session.Progress.SetStage(models.StageComplete)
		session.Status = models.SessionStatusFailed

		hotelData := map[string]interface{}{
			"error":    "session_lost",
			"message":  "Session lost - redirected to login page",
			"price":    float64(0),
			"currency": "US$",
		}

		resultID := ""
		if order != nil {
			resultID = order.ID
		}
		if resultID == "" {
			resultID = session.ID
		}

		result := models.NewResult(resultID, hotelData)
		session.Results = append(session.Results, result)
		s.sendProgress(session, 100, models.StageComplete, "Session lost - redirected to login")
		return
	}

	log.Printf("Session %s completed navigation to search", session.ID)

	select {
	case targetID := <-targetCreated:
		log.Printf("Session %s: Popup detected with target ID: %s", session.ID, targetID)
		popupCtx, popupCancel := chromedp.NewContext(listenCtx, chromedp.WithTargetID(target.ID(targetID)))
		cancelListen()
		if popupCtx == nil {
			log.Printf("Session %s: Failed to create popup context, continuing with main context", session.ID)
		} else {
			browserCtx = popupCtx
			_ = popupCancel
			log.Printf("Session %s: Switched to popup context", session.ID)
		}
	case <-time.After(5 * time.Second):
		cancelListen()
		log.Printf("Session %s: No popup opened within timeout, using main context", session.ID)
	}

	log.Printf("Session %s: Checking for date validation popup...", session.ID)
	if s.checkDatePopup(browserCtx) {
		log.Printf("Session %s: Date validation popup detected! Setting price to 0 and aborting.", session.ID)
		session.Progress.SetStage(models.StageComplete)
		session.Status = models.SessionStatusCompleted

		hotelData := map[string]interface{}{
			"error":    "date_validation_failed",
			"message":  "Date validation popup detected - dates may be in the past or too recent",
			"price":    float64(0),
			"currency": "US$",
		}

		resultID := ""
		if order != nil {
			resultID = order.ID
		}
		if resultID == "" {
			resultID = session.ID
		}

		result := models.NewResult(resultID, hotelData)
		session.Results = append(session.Results, result)
		s.sendProgress(session, 100, models.StageComplete, "Scraping aborted - date validation failed")
		return
	}

	// Wait for loading indicators to disappear, then check for completion
	var searchComplete bool
	for i := 0; i < 10; i++ {
		var loadingPresent bool
		chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				const loadingEl = document.querySelector('[class*="loading"], .spinner');
				return loadingEl !== null;
			})()`, &loadingPresent),
		)
		if loadingPresent {
			log.Printf("Session %s: Loading still present, waiting... (attempt %d/10)", session.ID, i+1)
			time.Sleep(1 * time.Second)
			continue
		}
		chromedp.Run(browserCtx,
			chromedp.Evaluate(`document.body.innerText.includes('Búsqueda completada')`, &searchComplete),
		)
		if searchComplete {
			log.Printf("Session %s: Search completed!", session.ID)
			break
		}
		log.Printf("Session %s: Waiting for search to complete... (attempt %d/10)", session.ID, i+1)
		time.Sleep(1 * time.Second)
	}

	time.Sleep(500 * time.Millisecond)

	session.Progress.SetStage(models.StageScraping)
	time.Sleep(500 * time.Millisecond)

	session.Progress.SetStage(models.StageScraping)
	s.sendProgress(session, 75, models.StageScraping, "Buscando hotel en menú lateral...")

	// Step 1: Wait for search to complete and input to be ready
	log.Printf("Session %s: Waiting for search to complete...", session.ID)
	time.Sleep(1 * time.Second)

	log.Printf("Session %s: Waiting for input to be ready...", session.ID)
	time.Sleep(1 * time.Second)

	// Extract hotel name from the order - remove (id: xxx) part
	searchHotelName := defaultAccommodationValue
	if strings.Contains(searchHotelName, "(id:") {
		if idx := strings.Index(searchHotelName, "(id:"); idx > 0 {
			searchHotelName = strings.TrimSpace(searchHotelName[:idx])
		}
	}
	// Escape single quotes for JavaScript
	escapedSearchName := strings.ReplaceAll(searchHotelName, "'", "\\'")
	log.Printf("Session %s: Hotel name to search: %s", session.ID, searchHotelName)

	// Simple approach: find the input at coordinates and type directly
	typeHotelScript := fmt.Sprintf(`(() => {
		const searchName = '%s'.trim().toLowerCase();
		console.log('Attempting to type: ' + searchName);
		
		// Try to find input by placeholder containing "alojamiento" or "hotel"
		let input = document.querySelector('input[placeholder*="alojamiento"]');
		if (!input) input = document.querySelector('input[placeholder*="Alojamiento"]');
		if (!input) input = document.querySelector('input[placeholder*="hotel"]');
		if (!input) input = document.querySelector('input[placeholder*="Hotel"]');
		
		// Try by name or id
		if (!input) input = document.querySelector('input[name*="hotel"]');
		if (!input) input = document.querySelector('input[id*="hotel"]');
		if (!input) input = document.querySelector('input[name*="accommodation"]');
		
		// Last resort: any visible text input in left part of screen
		if (!input) {
			const inputs = document.querySelectorAll('input[type="text"]');
			for (const inp of inputs) {
				const rect = inp.getBoundingClientRect();
				if (rect.left < 400 && rect.top > 100 && inp.offsetParent !== null && !inp.disabled) {
					input = inp;
					break;
				}
			}
		}
		
		if (!input) {
			// Try clicking at coordinates first to focus
			const x = 191, y = 599;
			const el = document.elementFromPoint(x, y);
			if (el && el.tagName === 'INPUT') {
				input = el;
			}
		}
		
		if (!input) {
			return 'INPUT_NOT_FOUND';
		}
		
		console.log('Found input, attempting to type with keyboard events...');
		
		// Focus the input
		input.focus();
		
		// Function to simulate typing a character
		const typeChar = (char) => {
			const keyCode = char.charCodeAt(0);
			
			// keydown event
			const ke1 = new KeyboardEvent('keydown', {
				key: char,
				code: 'Key' + char.toUpperCase(),
				keyCode: keyCode,
				which: keyCode,
				bubbles: true,
				cancelable: true
			});
			input.dispatchEvent(ke1);
			
			// keypress event (deprecated but some sites still use it)
			const ke2 = new KeyboardEvent('keypress', {
				key: char,
				code: 'Key' + char.toUpperCase(),
				keyCode: keyCode,
				which: keyCode,
				bubbles: true,
				cancelable: true
			});
			input.dispatchEvent(ke2);
			
			// Update value
			input.value += char;
			
			// input event
			const ie = new Event('input', { bubbles: true, cancelable: true });
			input.dispatchEvent(ie);
		};
		
		// Type each character
		for (let i = 0; i < searchName.length; i++) {
			typeChar(searchName[i]);
		}
		
		// keyup for last character
		const lastChar = searchName[searchName.length - 1];
		input.dispatchEvent(new KeyboardEvent('keyup', {
			key: lastChar,
			code: 'Key' + lastChar.toUpperCase(),
			bubbles: true
		}));
		
		// Change event
		input.dispatchEvent(new Event('change', { bubbles: true }));
		
		console.log('Typed with keyboard events: ' + input.value);
		return 'TYPED: ' + input.value;
	})()`, escapedSearchName)

	var typeResult string
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(typeHotelScript, &typeResult),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		log.Printf("Session %s: Error typing hotel name: %v", session.ID, err)
	}
	log.Printf("Session %s: Type result: [%s]", session.ID, typeResult)

	// Wait for autocomplete/dropdown to appear
	log.Printf("Session %s: Waiting for autocomplete dropdown...", session.ID)
	time.Sleep(1 * time.Second)

	// Debug: Check page state before clicking
	var pageState string
	chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			const input = document.querySelector('input[placeholder*="alojamiento"]');
			const dropdown = document.querySelector('.ui-autocomplete, [class*="autocomplete"], [role="listbox"]');
			const hotelLinks = document.querySelectorAll('a[id*="openHotel"], a[href*="/accommodation/"]');
			return JSON.stringify({
				inputValue: input ? input.value : 'NOT_FOUND',
				dropdownVisible: dropdown ? 'visible' : 'not_visible',
				dropdownChildren: dropdown ? dropdown.children.length : 0,
				hotelLinkCount: hotelLinks.length,
				bodyTextLength: document.body.innerText.length,
				url: window.location.href
			});
		})()`, &pageState),
	)
	log.Printf("Session %s: Page state before click: %s", session.ID, pageState)

	// Scroll to bottom to ensure all results are loaded
	var scrollResult string
	chromedp.Run(browserCtx,
		chromedp.Evaluate(`() => { window.scrollTo(0, document.body.scrollHeight); return 'scrolled'; }`, &scrollResult),
		chromedp.Sleep(2*time.Second),
	)

	// Wait more for results to load after scroll
	log.Printf("Session %s: Waiting for search results after scroll...", session.ID)
	time.Sleep(3 * time.Second)

	clickHotelFromResultsScript := fmt.Sprintf(`(() => {
		const searchName = '%s';
		const searchNameLower = searchName.trim().toLowerCase();
		console.log('Looking for hotel result:', searchName);
		
		// Scroll to bottom first
		window.scrollTo(0, document.body.scrollHeight);
		
		// First, check if autocomplete dropdown is visible
		const autocompleteSelectors = ['.ui-autocomplete', '[class*="autocomplete"]', '[role="listbox"]', '[class*="dropdown"]', '.ui-widget-content'];
		let dropdown = null;
		for (const sel of autocompleteSelectors) {
			const el = document.querySelector(sel);
			if (el && el.offsetParent !== null && el.children.length > 0) {
				dropdown = el;
				console.log('Found dropdown: ' + sel + ' with ' + el.children.length + ' children');
				break;
			}
		}
		
		// If autocomplete dropdown found, click first visible option
		if (dropdown) {
			const options = dropdown.querySelectorAll('li, a, div[class*="item"], div[class*="option"]');
			console.log('Dropdown options count:', options.length);
			for (const opt of options) {
				const rect = opt.getBoundingClientRect();
				if (rect.width > 0 && rect.height > 0 && opt.offsetParent !== null) {
					const text = opt.textContent || '';
					console.log('Clicking dropdown option:', text.substring(0, 50));
					opt.click();
					return 'CLICKED_DROPDOWN: ' + text.substring(0, 30);
				}
			}
			// If no styled options, try clicking the dropdown itself or first child
			if (dropdown.children.length > 0) {
				console.log('Clicking first dropdown child');
				dropdown.children[0].click();
				return 'CLICKED_DROPDOWN_CHILD';
			}
		}
		
		// No dropdown found - try pressing Enter to trigger search
		console.log('No dropdown found, pressing Enter to trigger search');
		const input = document.querySelector('input[placeholder*="alojamiento"]');
		if (input && input.value.length > 0) {
			input.focus();
			input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true }));
			input.dispatchEvent(new KeyboardEvent('keyup', { key: 'Enter', keyCode: 13, which: 13, bubbles: true }));
			return 'PRESSED_ENTER';
		}
		
		// Check if results are already showing on page (not in dropdown)
		// Look for hotel link by id
		const hotelLinkIds = ['openHotel2', 'openHotel3', 'openHotel4'];
		for (const id of hotelLinkIds) {
			const link = document.getElementById(id);
			if (link && link.offsetParent !== null) {
				console.log('Found hotel link: ' + id + ', clicking...');
				// Remove target="_blank" to open in same tab
				link.removeAttribute('target');
				link.click();
				return 'CLICKED_HOTEL: ' + id;
			}
		}
		
		// Also try by class
		const hotelLink = document.querySelector('.dev-hotel-title-name, .c-extended__title a, a[id*="openHotel"]');
		if (hotelLink) {
			console.log('Clicking hotel by class');
			hotelLink.click();
			return 'CLICKED_HOTEL_CLASS';
		}
		
		// Try by href pattern
		const hrefLinks = document.querySelectorAll('a[href*="/accommodation/"]');
		for (const link of hrefLinks) {
			if (link.offsetParent !== null && link.textContent.trim().length > 0) {
				console.log('Clicking hotel by href: ' + link.href);
				link.click();
				return 'CLICKED_BY_HREF';
			}
		}
		
		// Last resort: click on first link in left side
		const allLinks = document.querySelectorAll('a[href*="/accommodation/"]');
		for (const link of allLinks) {
			if (link.offsetParent !== null) {
				console.log('Clicking any accommodation link');
				link.click();
				return 'CLICKED_ANY';
			}
		}
		
		return 'NO_HOTEL_LINK_FOUND';
	})()`, escapedSearchName)

	var clickResult string
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(clickHotelFromResultsScript, &clickResult),
		chromedp.Sleep(3*time.Second),
		chromedp.WaitReady("body", chromedp.ByQuery),
	)
	if err != nil {
		log.Printf("Session %s: Error clicking hotel: %v", session.ID, err)
	}
	log.Printf("Session %s: Click result: [%s]", session.ID, clickResult)

	// If not clicked yet, use calculated coordinates
	// From center of screen, move down 50% of distance to bottom = 75% of screen height
	if clickResult == "" || clickResult == "NO_HOTEL_LINK_FOUND" || strings.Contains(clickResult, "NOT_FOUND") {
		log.Printf("Session %s: Using calculated click from center to 75%% height...", session.ID)

		// Get window size and calculate click position
		var windowSize map[string]int
		chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				return { width: window.innerWidth, height: window.innerHeight };
			})()`, &windowSize),
		)

		clickX := float64(windowSize["width"]) / 2
		clickY := float64(windowSize["height"]) * 0.75

		log.Printf("Session %s: Clicking at calculated position (%.0f, %.0f)", session.ID, clickX, clickY)

		chromedp.Run(browserCtx,
			chromedp.MouseClickXY(clickX, clickY),
			chromedp.Sleep(1*time.Second),
		)
	} else if clickResult == "PRESSED_ENTER" {
		log.Printf("Session %s: Enter pressed, waiting for results to load...", session.ID)
		time.Sleep(3 * time.Second)
		// After Enter, try to find and click a hotel link
		var hotelLinks []string
		chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				const links = document.querySelectorAll('a[href*="/accommodation/"]');
				return Array.from(links).map(l => l.href + '|' + l.textContent.trim().substring(0, 50)).slice(0, 5);
			})()`, &hotelLinks),
		)
		log.Printf("Session %s: Found hotel links after Enter: %v", session.ID, hotelLinks)
	}

	log.Printf("Session %s: Waiting for rooms to load...", session.ID)
	for i := 0; i < 10; i++ {
		var roomsLoaded map[string]interface{}
		err = chromedp.Run(browserCtx,
			chromedp.Evaluate(`(() => {
				const text = document.body.innerText;
				const loading = text.includes('Estamos buscando los mejores precios');
				const ready = text.includes('Opciones de reserva');
				return { loading, ready };
			})()`, &roomsLoaded),
		)
		if err == nil && roomsLoaded != nil {
			if ready, ok := roomsLoaded["ready"].(bool); ok && ready {
				log.Printf("Session %s: Rooms loaded successfully!", session.ID)
				break
			}
		}
		log.Printf("Session %s: Waiting for rooms... (attempt %d/10)", session.ID, i+1)
		time.Sleep(1 * time.Second)
	}

	var hotelPageCheck string
	checkHotelScript := fmt.Sprintf(`(() => {
		const expectedName = '%s'.replace(/\s*\(id:\s*\d+\)/, '').trim().toLowerCase();
		
		const url = window.location.href;
		const bodyText = document.body.innerText;
		
		const enResultados = bodyText.includes('Búsqueda completada');
		
		const nameEl = document.querySelector('.dev-hotel-title-name') || 
		               document.querySelector('.c-extended__title') ||
		               document.querySelector('.hotel-name.dev-hotel-title') ||
		               document.querySelector('[class*="hotel-title"]');
		
		let foundName = '';
		if (nameEl) {
			foundName = nameEl.innerText.trim().toLowerCase();
		}
		
		if (!foundName) {
			const hotelIdEl = document.getElementById('openHotel2');
			if (hotelIdEl) {
				foundName = hotelIdEl.innerText.trim().toLowerCase();
			}
		}
		
		const opcionesReserva = bodyText.includes('Opciones de reserva');
		
		if (enResultados) {
			return 'STILL IN RESULTS - searching for hotel: ' + foundName;
		}
		
		// Only consider it a match if foundName is not empty AND names match
		const namesMatch = foundName.length > 0 && (foundName.includes(expectedName) || expectedName.includes(foundName));
		if (namesMatch) {
			return 'URL: ' + url + ' | HOTEL: ' + foundName + ' | Opciones: ' + opcionesReserva;
		}
		
		return 'UNKNOWN_PAGE - url: ' + url;
	})()`, defaultAccommodationValue)

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(checkHotelScript, &hotelPageCheck),
	)
	if err != nil {
		log.Printf("Session %s: Error checking hotel: %v", session.ID, err)
	}

	log.Printf("Session %s: Hotel page check: [%s]", session.ID, hotelPageCheck)

	skipAbort := false

	if strings.Contains(hotelPageCheck, "STILL IN RESULTS") || strings.Contains(hotelPageCheck, "UNKNOWN_PAGE") {
		log.Printf("Session %s: Hotel page check shows problem: %s - trying direct hotel navigation", session.ID, hotelPageCheck)

		// Try direct navigation to hotel with availability using tripId
		if order != nil && order.HotelID != "" {
			// Try with tripId=0 for basic availability
			hotelDirectURL := fmt.Sprintf("https://www.delfos.tur.ar/accommodation/%s/available/1?tripId=0", order.HotelID)
			log.Printf("Session %s: Attempting direct navigation to hotel with availability: %s", session.ID, hotelDirectURL)

			var directNavResult bool
			err = chromedp.Run(browserCtx,
				chromedp.Navigate(hotelDirectURL),
				chromedp.Sleep(10*time.Second),
				chromedp.Evaluate(`document.body.innerText.includes('Opciones de reserva')`, &directNavResult),
			)
			if err == nil && directNavResult {
				log.Printf("Session %s: Direct navigation successful! Opciones de reserva found - continuing", session.ID)
				skipAbort = true
			} else {
				log.Printf("Session %s: Direct navigation did not find Opciones de reserva", session.ID)
			}
		}

		if !skipAbort {
			time.Sleep(1 * time.Second)
			if s.checkDatePopup(browserCtx) {
				log.Printf("Session %s: Date validation popup confirmed! Setting price to 0 and aborting.", session.ID)

				session.Progress.SetStage(models.StageComplete)
				session.Status = models.SessionStatusCompleted

				hotelData := map[string]interface{}{
					"error":    "date_validation_failed",
					"message":  "Failed to open hotel page - date validation popup blocked navigation",
					"price":    float64(0),
					"currency": "US$",
				}

				resultID := ""
				if order != nil {
					resultID = order.ID
				}
				if resultID == "" {
					resultID = session.ID
				}

				result := models.NewResult(resultID, hotelData)
				session.Results = append(session.Results, result)
				s.sendProgress(session, 100, models.StageComplete, "Scraping aborted - date validation failed")
				return
			}

			// Instead of aborting, continue with extraction from whatever page we're on
			log.Printf("Session %s: Hotel page not confirmed, but continuing with extraction anyway...", session.ID)
		}
	}

	time.Sleep(1 * time.Second)

	var roomsButtonResult string
	roomsButtonScript := `(() => {
		const allIds = [];
		document.querySelectorAll('[id]').forEach(el => {
			const id = el.id;
			if (id.toLowerCase().includes('accommodation') || 
				id.toLowerCase().includes('section') ||
				id.toLowerCase().includes('mobile') ||
				id.toLowerCase().includes('habitacion')) {
				allIds.push(id);
			}
		});
		
		const sectionPanel = document.getElementById('accommodationSectionPanel');
		
		if (sectionPanel) {
			const buttons = sectionPanel.querySelectorAll('button, a');
			for (const btn of buttons) {
				const text = (btn.textContent || '').toLowerCase().trim();
				if (text.includes('habitaciones')) {
					btn.click();
					return 'CLICKED: HABITACIONES (section panel)';
				}
			}
		}
		
		const mobileNav = document.getElementById('accommodation-detail:c-mobile-navigation');
		
		if (mobileNav) {
			const buttons = mobileNav.querySelectorAll('button, a');
			for (const btn of buttons) {
				const text = (btn.textContent || '').toLowerCase().trim();
				if (text.includes('ver habitaciones')) {
					btn.click();
					return 'CLICKED: Ver habitaciones (mobile nav)';
				}
			}
		}
		
		const allButtons = document.querySelectorAll('button, a');
		for (const btn of allButtons) {
			const text = (btn.textContent || '').toLowerCase().trim();
			if (text.includes('habitaciones')) {
				btn.click();
				return 'CLICKED: Habitaciones (fallback)';
			}
		}
		
		return 'NO CLICK - IDs found: ' + JSON.stringify(allIds.slice(0, 10));
	})()`

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(roomsButtonScript, &roomsButtonResult),
	)
	if err == nil {
		log.Printf("Session %s: Rooms button: [%s]", session.ID, roomsButtonResult)
	}

	time.Sleep(1 * time.Second)

	hotelData := s.extractCurrentHotelWithOrder(browserCtx, session, roomType, mealPlan)

	time.Sleep(500 * time.Millisecond)

	s.sendProgress(session, 90, models.StageProcessing, "Procesando resultados...")

	// Try to send price to API if we have an order
	if s.apiClient != nil && order != nil && hotelData["room"] != nil {
		if roomData, ok := hotelData["room"].(map[string]interface{}); ok {
			if mealPlanData, ok := roomData["mealPlan"].(map[string]interface{}); ok {
				if currentPrice, ok := mealPlanData["price"].(float64); ok {
					log.Printf("Session %s: Sending price to API: %.2f", session.ID, currentPrice)
					result, err := s.apiClient.UpdatePrice(order.ID, currentPrice, roomType, mealPlan)
					if err != nil {
						log.Printf("Session %s: Failed to update price in API: %v", session.ID, err)
					} else {
						log.Printf("Session %s: Price updated in API. Diff: %.2f (%.1f%%), Savings: %v",
							session.ID, result.PriceDifference, result.PriceDifferencePct, result.Savings)
						hotelData["price_comparison"] = result
					}
				}
			}
		}
	}

	time.Sleep(500 * time.Millisecond)

	// Add booked price to the result data for comparison
	hotelData["bookedPrice"] = session.BookedPrice

	// Use hotel name from order if available
	if session.Order != nil && session.Order.HotelName != "" {
		hotelData["hotelName"] = session.Order.HotelName
	}

	// Add passengers distribution
	hotelData["passengers"] = ""
	if session.Order != nil && session.Order.Passengers != "" {
		hotelData["passengers"] = session.Order.Passengers
	}

	// Get result ID safely
	resultID := ""
	if order != nil {
		resultID = order.ID
	}
	if resultID == "" {
		resultID = session.ID
	}

	result := models.NewResult(resultID, hotelData)
	if screenshot, ok := hotelData["screenshot"].(string); ok && screenshot != "" {
		result.Screenshot = screenshot
	}
	session.Results = append(session.Results, result)
	session.Status = models.SessionStatusCompleted
	session.Progress.SetProgress(100)
	session.Progress.SetStage(models.StageComplete)

	ws.SendToSession(session.ID, session.Progress)
	log.Printf("Session %s completed successfully", session.ID)
}

func (s *ScraperService) extractCurrentHotel(ctx context.Context, session *ScraperSession) map[string]interface{} {
	var result map[string]interface{}
	script := `(() => {
		console.log('=== EXTRACT CURRENT HOTEL DEBUG ===');
		const url = window.location.href;
		const bodyText = document.body.innerText;
		
		const hotelEl = document.getElementById('hotel');
		const nameEl = hotelEl ? hotelEl.querySelector('.hotel-name') : null;
		const hotelName = nameEl ? nameEl.innerText.trim() : 'Not found';
		console.log('Hotel name:', hotelName);
		
		const bookOptionsDiv = document.getElementById('booking-options:bookOptions');
		console.log('bookOptionsDiv found:', !!bookOptionsDiv);
		
		const roomMap = {};
		let matchingPanelIndex = -1;
		let panelCounter = 0;
		
		if (bookOptionsDiv) {
			const combinations = bookOptionsDiv.querySelectorAll('.o-box.u-border-radius--big');
			console.log('Combinations found:', combinations.length);
			
			// Also log the raw HTML of the first combination to see structure
			if (combinations.length > 0) {
				console.log('First combination HTML:', combinations[0].outerHTML.substring(0, 500));
			}
			
			combinations.forEach((el, i) => {
				const roomNameEl = el.querySelector('.hotel-cancellation-policies, [class*="habitaci"], [class*="room"]');
				const roomName = roomNameEl ? roomNameEl.innerText.trim() : '';
				
				const mealPlanEl = el.querySelector('.dev-mealplan-mapping .dev-mealplan');
				const mealPlan = mealPlanEl ? mealPlanEl.innerText.trim() : '';
				
				const priceEl = el.querySelector('.dev-combination-price p') || el.querySelector('.dev-combination-price') || el.querySelector('.dev-combination-row-details');
				const priceText = priceEl ? priceEl.innerText.trim() : '';
				
				console.log('Panel', i, '- roomName:', roomName, 'mealPlan:', mealPlan, 'priceText:', priceText);
				
				const currencyMatch = priceText.match(/US?\$/);
				const currency = currencyMatch ? currencyMatch[0] : '$';
				
				// Parse price correctly for Argentine format (1.500,00 = 1500.00)
				const cleanedPrice = priceText.replace(/[^0-9,]/g, "").replace(/\\./g, "").replace(/,/g, ".");
				const price = cleanedPrice ? parseFloat(cleanedPrice) : 0;
				
				const cancellationEl = el.querySelector('.freecancellation, .cancellation-policy');
				const cancellation = cancellationEl ? cancellationEl.innerText.trim() : '';
				
				const cancellationSummaryEl = el.querySelector('[class*="cancellation"], .cancellation-summary, .freecancellation-text');
				const cancellationSummary = cancellationSummaryEl ? cancellationSummaryEl.innerText.trim() : '';
				
				if (roomName && mealPlan) {
					if (!roomMap[roomName]) {
						roomMap[roomName] = { room: roomName, mealPlans: [], panelIndex: i };
					}
					roomMap[roomName].mealPlans.push({
						plan: mealPlan,
						price: price,
						currency: currency,
						cancellation: cancellation,
						cancellationSummary: cancellationSummary,
						panelIndex: i
					});
				}
			});
		} else {
			console.log('ERROR: bookOptionsDiv not found!');
			// Try to find alternative selectors
			const allDivs = document.querySelectorAll('div[id*="booking"], div[id*="option"], div[class*="combination"]');
			console.log('Alternative divs found:', allDivs.length);
			allDivs.forEach((d, i) => {
				if (i < 3) console.log('Alt div', i, ':', d.id, d.className);
			});
		}
		
		const rooms = Object.values(roomMap);
		console.log('Rooms in roomMap:', rooms.length, rooms);
		
		const roomType = '';
		const roomMealPlan = '';
		
		// Debug: Show all available rooms before filtering
		console.log('=== ALL AVAILABLE ROOMS ===');
		rooms.forEach((r, idx) => {
			console.log('Room', idx, ':', r.room);
			r.mealPlans.forEach((mp, mpIdx) => {
				console.log('  MealPlan', mpIdx, ':', mp.plan, '- Price:', mp.price);
			});
		});
		console.log('=== END ROOMS ===');
		
		// Simple filter - use first room always (for MVP)
		const filteredRooms = rooms.length > 0 ? [rooms[0]] : [];
		console.log('Using first room:', filteredRooms[0]?.room);
		
		// Also return all rooms for later filtering
		const allRooms = rooms;
		
		let foundPanelIndex = -1;
		if (filteredRooms.length > 0 && filteredRooms[0].mealPlans.length > 0) {
			foundPanelIndex = filteredRooms[0].mealPlans[0].panelIndex;
		}
		
		const debug = {
			url: url,
			hotelName: hotelName,
			roomCount: filteredRooms.length,
			roomsFound: rooms.length,
			allRooms: rooms,
			panelIndex: foundPanelIndex,
			// DEBUG INFO
			debug_bookOptionsFound: !!bookOptionsDiv,
			debug_combinationsCount: bookOptionsDiv ? bookOptionsDiv.querySelectorAll('.o-box.u-border-radius--big').length : 0,
			debug_roomMapSize: Object.keys(roomMap).length,
			debug_roomsArray: rooms,
			debug_firstCombinationClasses: bookOptionsDiv && bookOptionsDiv.querySelector('.hotelCombinationPanel') ? 
				bookOptionsDiv.querySelector('.hotelCombinationPanel').className : 'not found',
			// END DEBUG
			room: filteredRooms.length > 0 ? {
				cancellation: filteredRooms[0].mealPlans[0]?.cancellation || "","
				roomName: filteredRooms[0].room,
				mealPlan: filteredRooms[0].mealPlans[0]
			} : null
		};
		
		return debug;
	})()`

	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &result)); err != nil {
		log.Printf("Session %s: extractCurrentHotel error: %v", session.ID, err)
		return map[string]interface{}{"error": err.Error()}
	}

	log.Printf("Session %s: Current hotel: %s, Rooms found: %v, panelIndex: %v", session.ID, result["hotelName"], result["roomCount"], result["panelIndex"])
	log.Printf("Session %s: DEBUG - result['room'] = %v, type = %T", session.ID, result["room"], result["room"])

	hasValidPrice := false

	if result["room"] != nil {
		if roomData, ok := result["room"].(map[string]interface{}); ok {
			if mealPlan, ok := roomData["mealPlan"].(map[string]interface{}); ok {
				if price, ok := mealPlan["price"].(float64); ok && price > 0 {
					hasValidPrice = true
					maxPrice := session.BookedPrice
					log.Printf("Session %s: Valid price from room.mealPlan.price: %.2f, bookedPrice: %.2f", session.ID, price, maxPrice)
				}
			}
		}
	}

	// Validate price against BookedPrice - flag suspicious prices
	bookedPrice := session.BookedPrice
	if hasValidPrice && bookedPrice > 0 {
		var foundPrice float64
		if result["room"] != nil {
			if roomData, ok := result["room"].(map[string]interface{}); ok {
				if mealPlan, ok := roomData["mealPlan"].(map[string]interface{}); ok {
					if price, ok := mealPlan["price"].(float64); ok {
						foundPrice = price
					}
				}
			}
		}
		if foundPrice > 0 {
			ratio := foundPrice / bookedPrice
			if ratio < 0.3 {
				log.Printf("Session %s: WARNING - Price %.2f is suspiciously low (%.0f%% of booked price %.2f). Rejecting.",
					session.ID, foundPrice, ratio*100, bookedPrice)
				hasValidPrice = false
				result["priceRejected"] = true
				result["priceRejectReason"] = "too_low"
			} else if ratio > 2.5 {
				log.Printf("Session %s: WARNING - Price %.2f is suspiciously high (%.0f%% of booked price %.2f). Rejecting.",
					session.ID, foundPrice, ratio*100, bookedPrice)
				hasValidPrice = false
				result["priceRejected"] = true
				result["priceRejectReason"] = "too_high"
			}
		}
	}

	if !hasValidPrice {
		if currentPrice, ok := result["currentPrice"].(float64); ok && currentPrice > 0 {
			// Also validate fallback price
			if bookedPrice > 0 {
				ratio := currentPrice / bookedPrice
				if ratio < 0.3 || ratio > 2.5 {
					log.Printf("Session %s: Rejecting fallback price %.2f (ratio: %.0f%% of booked %.2f)",
						session.ID, currentPrice, ratio*100, bookedPrice)
				} else {
					hasValidPrice = true
					log.Printf("Session %s: Valid price from currentPrice fallback: %.2f", session.ID, currentPrice)
				}
			} else {
				hasValidPrice = true
				log.Printf("Session %s: Valid price from currentPrice fallback: %.2f", session.ID, currentPrice)
			}
		}
	}

	s.captureElementScreenshot(ctx, session, result)

	return result
}

// extractCurrentHotelWithOrder extracts hotel data with specific room type and meal plan from order
func (s *ScraperService) extractCurrentHotelWithOrder(ctx context.Context, session *ScraperSession, roomType, mealPlan string) map[string]interface{} {
	var result map[string]interface{}

	log.Printf("Session %s: Waiting briefly for 'Opciones de reserva' or scrolling to find prices...", session.ID)
	timeout := 30 * time.Second
	deadline := time.Now().Add(timeout)

	// Wait a short time for 'Opciones de reserva' to appear, but don't require it
	for time.Now().Before(deadline) {
		var opcionesReserva bool
		chromedp.Run(ctx,
			chromedp.Evaluate(`document.body.innerText.includes('Opciones de reserva')`, &opcionesReserva),
		)
		if opcionesReserva {
			log.Printf("Session %s: 'Opciones de reserva' found, proceeding with extraction", session.ID)
			break
		}
		// Scroll a bit to help load content
		var scrollResult string
		chromedp.Run(ctx,
			chromedp.Evaluate(`window.scrollBy(0, 500); return 'scrolled';`, &scrollResult),
		)
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for prices to appear (they load dynamically after page interaction)
	log.Printf("Session %s: Waiting for prices to load (up to 2 minutes)...", session.ID)
	priceWaitTimeout := 120 * time.Second
	priceWaitDeadline := time.Now().Add(priceWaitTimeout)
	priceFound := false
	lastPriceCheck := time.Now()

	for time.Now().Before(priceWaitDeadline) {
		var bodyText string
		chromedp.Run(ctx,
			chromedp.Evaluate(`document.body.innerText`, &bodyText),
		)

		// Check if prices are present
		if strings.Contains(bodyText, "US$") || strings.Contains(bodyText, "USD") {
			// Count price occurrences - if we have many, prices have likely loaded
			priceCount := strings.Count(bodyText, "US$")
			if priceCount >= 3 {
				priceFound = true
				log.Printf("Session %s: Prices detected in page (%d occurrences), proceeding with extraction", session.ID, priceCount)
				break
			}
		}

		// Scroll to trigger lazy loading
		var scrollResult string
		chromedp.Run(ctx,
			chromedp.Evaluate(`window.scrollBy(0, 500);`, &scrollResult),
		)
		time.Sleep(500 * time.Millisecond)

		// Log progress every 30 seconds
		if time.Since(lastPriceCheck) > 30*time.Second {
			log.Printf("Session %s: Still waiting for prices... (%.0f seconds elapsed)", session.ID, time.Since(priceWaitDeadline.Add(-priceWaitTimeout)).Seconds())
			lastPriceCheck = time.Now()
		}
	}

	if !priceFound {
		log.Printf("Session %s: Warning - prices may not have loaded, continuing anyway", session.ID)
	}

	script := `(() => {
		var results = [];

		// Scroll to bottom multiple times to ensure all content is loaded
		for (var i = 0; i < 5; i++) {
			window.scrollTo(0, document.body.scrollHeight);
		}

		// Wait for "Opciones de reserva" section to be visible
		var opcionesEl = document.querySelector('[id*="bookOptions"], .book-options, #booking-options');
		if (!opcionesEl) {
			console.log('Book options element not found');
		}

		// FIRST: Look for TOTAL price at the bottom of the page (this is the real price)
		// Look for elements that typically contain total/sum prices
		var totalPriceEl = null;
		var totalSelectors = [
			'.total-price', '.price-total', '.grand-total', '.final-price',
			'[class*="total"]', '[class*="sum"]', '[class*="final"]',
			'.dev-total-price', '.price-summary', '.booking-total',
			'.u-text--bold', 'strong', 'b'
		];

		for (var i = 0; i < totalSelectors.length; i++) {
			var els = document.querySelectorAll(totalSelectors[i]);
			els.forEach(function(el) {
				var text = el.innerText || '';
				// Look for patterns like "Total: US$ 1,234" or "US$1,234" in large/bold text
				if (text.match(/total.*US\$/i) || text.match(/US\$\s*[\d,]+/) && text.length < 100) {
					var priceMatch = text.match(/US\$\s*([\d,]+)/);
					if (priceMatch) {
						var price = parseFloat(priceMatch[1].replace(/,/g, ''));
						// Only consider reasonable total prices (not per-room)
						if (price > 1000 && price < 1000000) {
							totalPriceEl = el;
							console.log('Found total price element:', text.trim().substring(0, 100));
						}
					}
				}
			});
			if (totalPriceEl) break;
		}

		// Try to find prices in specific containers first
		var priceContainers = document.querySelectorAll('.dev-combination-price, .hotel-combination-price, [class*="price-wrapper"]');
		console.log('Price containers found:', priceContainers.length);

		// Look for specific price patterns like "Precio de venta" or US$ followed by numbers
		var specificPricePattern = /Precio de venta[:\s]*US?\$\s*([\d,]+)/i;
		var bodyText = document.body.innerText;

		// Check for "Precio de venta" pattern first
		var match = bodyText.match(specificPricePattern);
		if (match) {
			var price = parseFloat(match[1].replace(/,/g, ''));
			console.log('Found specific price (Precio de venta): US$', price);
			results.push({
				mealplan: 'Precio de venta',
				price: price,
				source: 'Precio de venta pattern',
				index: results.length
			});
		}

		// NEW: Find prices by room type - look for container with room name AND price
		// The room type from order is "2 V ROOMS double" - we need to find matching container
		var roomTypePattern = /2\s*V\s*ROOMS|Double\s*Room|Habitaci/i;
		var allContainers = document.querySelectorAll('[class*="room"], [class*="option"], [class*="card"]');

		allContainers.forEach(function(container) {
			var containerText = container.innerText || '';
			if (!containerText.match(roomTypePattern)) return;
			if (containerText.length < 10 || containerText.length > 500) return;

			var priceMatch = containerText.match(/US\$\s*([\d,]+)/);
			if (!priceMatch) return;

			var price = parseFloat(priceMatch[1].replace(/,/g, ''));
			if (price < 100 || price > 100000) return;

			// Determine room type from container
			var roomName = '';
			if (containerText.match(/2\s*V\s*ROOMS/i)) roomName = '2 V ROOMS double';
			else if (containerText.match(/double/i)) roomName = 'Double Room';
			else if (containerText.match(/habitaci/i)) roomName = 'Habitacion';

			results.push({
				mealplan: roomName || 'UNKNOWN',
				price: price,
				source: 'room-type-match',
				index: results.length
			});
			console.log('Found price for room type:', roomName, '- US$', price);
		});

		// If no room-type matched prices, fall back to finding all prices
		if (results.length === 0) {
			console.log('No room-type matched prices, falling back to all div prices');
			var seenPrices = {};

			var allDivs = document.querySelectorAll('div');

			allDivs.forEach(function(div) {
				var divText = div.innerText;
				if (!divText || divText.length < 10) return;

				// Look for US$ pattern in div text
				var priceMatch = divText.match(/US\$\s*([\d,]+)/);
				if (!priceMatch) return;

				var priceStr = priceMatch[1];
				if (seenPrices[priceStr]) return;
				seenPrices[priceStr] = true;

				// Validate this looks like a real price (not just any text with US$)
				var cleanText = divText.replace(/\s+/g, ' ').trim();
				if (cleanText.length > 200) return; // Skip if text too long (likely not a price container)

				var mealplan = '';
				var mpEl = div.querySelector('.dev-mealplan');
				if (mpEl) {
					mealplan = mpEl.innerText.trim();
				}

				// Infer meal plan from text if not found
				if (!mealplan) {
					var textLower = divText.toLowerCase();
					if (textLower.indexOf('solo habitaci') > -1) mealplan = 'SOLO HABITACIÓN';
					else if (textLower.indexOf('desayuno') > -1) mealplan = 'CON DESAYUNO';
					else if (textLower.indexOf('media pensi') > -1) mealplan = 'MEDIA PENSIÓN';
					else if (textLower.indexOf('all inclusive') > -1) mealplan = 'ALL INCLUSIVE';
				}

				var price = parseFloat(priceStr.replace(/,/g, ''));

				if (price > 0 && price < 100000) { // Reasonable price range
					results.push({
						mealplan: mealplan,
						price: price,
						source: 'div container',
						index: results.length
					});
					console.log('Found option:', mealplan, '- US$', price);
				}
			});
		}

		// Fallback to general search
		if (results.length === 0) {
			var priceMatches = bodyText.match(/US\$\s*([\d,]+)/g);
			console.log('Fallback price matches:', priceMatches ? priceMatches.length : 0);

			if (priceMatches && priceMatches.length > 0) {
				var seen = {};
				priceMatches.forEach(function(m) {
					var num = m.replace(/[^\d,]/g, '');
					if (seen[num]) return;
					seen[num] = true;
					var price = parseFloat(num.replace(/,/g, ''));
					if (price > 0 && price < 100000) {
						results.push({mealplan: '', price: price, source: 'fallback', index: results.length});
					}
				});
			}
		}

		var hotelEl = document.querySelector('h1');
		var hotelName = hotelEl ? hotelEl.innerText.trim() : 'Unknown';

		// Get URL for debugging
		var url = window.location.href;

		console.log('Final results:', results.length, 'at URL:', url);

		return {
			hotelName: hotelName,
			url: url,
			options: results,
			totalPrice: totalPriceEl ? parseFloat(totalPriceEl.innerText.match(/US\$\s*([\d,]+)/)[1].replace(/,/g, '')) : 0
		};
	})();`

	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &result)); err != nil {
		log.Printf("Session %s: Extract error: %v", session.ID, err)
		return map[string]interface{}{"error": err.Error()}
	}

	var optionsCount int
	if result["options"] != nil {
		optionsCount = len(result["options"].([]interface{}))
	}
	log.Printf("Session %s: Extracted %d options, hotel: %v, url: %v", session.ID, optionsCount, result["hotelName"], result["url"])

	bookedPrice := session.BookedPrice

	// Check if we found a total price at the bottom of the page
	totalPrice, hasTotalPrice := result["totalPrice"].(float64)
	if hasTotalPrice && totalPrice > 0 {
		log.Printf("Session %s: Found total price at bottom of page: %.2f", session.ID, totalPrice)
		result["currentPrice"] = totalPrice
		result["priceSource"] = "totalPrice"
	} else if optionsCount > 0 {
		if options, ok := result["options"].([]interface{}); ok && len(options) > 0 {
			selectedPrice := options[0].(map[string]interface{})["price"].(float64)
			selectedSource := options[0].(map[string]interface{})["source"].(string)
			result["currentPrice"] = selectedPrice
			result["selectedMealPlan"] = options[0].(map[string]interface{})["mealplan"]
			result["priceSource"] = "options"

			// If we found the largest price on page, skip the ratio check (it's likely the total)
			if selectedSource == "largest price on page" {
				log.Printf("Session %s: Using largest price on page: %.2f (skipping ratio check)", session.ID, selectedPrice)
			} else if bookedPrice > 0 {
				ratio := selectedPrice / bookedPrice
				if ratio < 0.3 {
					log.Printf("Session %s: WARNING - Price %.2f is suspiciously low (%.0f%% of booked price %.2f). Trying next option.",
						session.ID, selectedPrice, ratio*100, bookedPrice)
					if len(options) > 1 {
						selectedPrice = options[1].(map[string]interface{})["price"].(float64)
						result["currentPrice"] = selectedPrice
						result["selectedMealPlan"] = options[1].(map[string]interface{})["mealplan"]
						result["priceSource"] = "options-fallback"
						log.Printf("Session %s: Using second option price: %.2f", session.ID, selectedPrice)
					} else {
						log.Printf("Session %s: No alternative price available", session.ID)
					}
				} else if ratio > 2.5 {
					log.Printf("Session %s: WARNING - Price %.2f is suspiciously high (%.0f%% of booked price %.2f). Trying next option.",
						session.ID, selectedPrice, ratio*100, bookedPrice)
					if len(options) > 1 {
						selectedPrice = options[1].(map[string]interface{})["price"].(float64)
						result["currentPrice"] = selectedPrice
						result["selectedMealPlan"] = options[1].(map[string]interface{})["mealplan"]
						result["priceSource"] = "options-fallback"
						log.Printf("Session %s: Using second option price: %.2f", session.ID, selectedPrice)
					}
				} else {
					log.Printf("Session %s: Selected price: %.2f (ratio: %.0f%% of booked %.2f)", session.ID, selectedPrice, ratio*100, bookedPrice)
				}
			} else {
				log.Printf("Session %s: Selected price: %.2f (no booked price for comparison)", session.ID, selectedPrice)
			}

			// Skip price multiplication if we already found the largest price (it's likely the total)
			if selectedSource == "largest price on page" {
				log.Printf("Session %s: Largest price on page detected, using it directly without multiplication", session.ID)
			} else if session.Order != nil {
				checkIn, _ := time.Parse("2006-01-02T15:04:05Z", session.Order.CheckIn)
				checkOut, _ := time.Parse("2006-01-02T15:04:05Z", session.Order.CheckOut)
				nights := int(checkOut.Sub(checkIn).Hours() / 24)

				rooms := 1
				if strings.Contains(session.Order.SearchURL, "distribution=") {
					distStart := strings.Index(session.Order.SearchURL, "distribution=") + 12
					distEnd := strings.Index(session.Order.SearchURL[distStart:], "&")
					if distEnd > 0 {
						dist := session.Order.SearchURL[distStart : distStart+distEnd]
						if idx := strings.Index(dist, "~~"); idx > 0 {
							if adults, err := strconv.Atoi(dist[:idx]); err == nil {
								rooms = (adults + 1) / 2
							}
						}
					}
				}

				if nights > 0 && rooms > 0 && selectedPrice > 0 {
					totalPrice := selectedPrice * float64(nights) * float64(rooms)
					result["currentPrice"] = totalPrice
					log.Printf("Session %s: Adjusted price from %.2f (per night/room) to %.2f (total for %d nights × %d rooms)",
						session.ID, selectedPrice, totalPrice, nights, rooms)
				}
			}
		}
	}

	s.captureElementScreenshot(ctx, session, result)

	return result
}

func (s *ScraperService) captureElementScreenshot(ctx context.Context, session *ScraperSession, result map[string]interface{}) {
	log.Printf("Session %s: Starting screenshot capture", session.ID)

	var screenshot []byte

	// Simply capture full page screenshot - most reliable
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&screenshot, 100)); err == nil {
		result["screenshot"] = base64.StdEncoding.EncodeToString(screenshot)
		log.Printf("Session %s: Full page screenshot captured, size: %d bytes", session.ID, len(screenshot))
	} else {
		log.Printf("Session %s: Screenshot capture failed: %v", session.ID, err)
	}
}

func (s *ScraperService) extractHotels(ctx context.Context, session *ScraperSession) map[string]interface{} {
	var result map[string]interface{}
	script := `(() => {
		try {
			const cookies = document.cookie;
			const url = window.location.href;
			const title = document.title;
			
			const hotels = [];
			const seen = new Set();
			
			const hotelElements = document.querySelectorAll('[id^="mainForm:datascrollHorizontal:"][id$=":hotelextended:otherOption"]');
			
			console.log('Found hotel elements: ' + hotelElements.length);
			
			hotelElements.forEach((el) => {
				const innerText = el.innerText || '';
				
				const lines = innerText.split('\n').map(l => l.trim()).filter(l => l.length > 0);
				let name = '';
				
				for (const line of lines) {
					const lower = line.toLowerCase();
					if (lower.includes('ver alojamiento') || lower.includes('seleccionar') || 
						lower.includes('mostrar en el mapa') || lower.includes('ver opciones') ||
						lower.includes('solo habitación') || lower.includes('cancelacion') ||
						lower.includes('expedia') || lower.includes('delfos') ||
						lower.includes('precio') || lower.includes('total') ||
						/^\d+/.test(line) || /^\$/.test(line) || /US\$/.test(line)) {
						continue;
					}
					if (lower.includes('hotel') || lower.includes('inn') || 
						lower.includes('resort') || lower.includes('village') || lower.includes('boutique') ||
						lower.includes('place') || lower.includes('manor') || lower.includes('beach') ||
						lower.includes('park') || lower.includes('palace')) {
						name = line;
						break;
					}
				}
				
				if (!name && lines.length > 0) {
					for (const line of lines) {
						const lower = line.toLowerCase();
						if (!lower.includes('ver') && !lower.includes('seleccionar') && line.length > 3) {
							name = line;
							break;
						}
					}
				}
				
				let price = 'N/A';
				const priceMatch = innerText.match(/(?:Total:|Precio total|US\$)\s*([\d,]+)/i);
				if (priceMatch) {
					price = 'US$' + priceMatch[1];
				} else {
					const usdMatch = innerText.match(/US\$[\d,]+/);
					if (usdMatch) {
						price = usdMatch[0];
					}
				}
				
				if (name) {
					name = name.replace(/[<>]/g, '').trim();
				}
				
				if (name && name.length > 2 && !seen.has(name)) {
					seen.add(name);
					hotels.push({ name, price });
				}
			});
			
			console.log('Extracted hotels: ' + hotels.length);
			
			if (hotels.length === 0) {
				const allText = document.body.innerText;
				const hotelPrices = allText.match(/([A-Z][A-Za-z\s&'-]+(?:Hotel|Inn|Resort|Village|Boutique|Place|Manor|Beach|Park)[A-Za-z\s&'-]*)\s*(?:Total:|US\$|Precio)[\s:]*(\$?[\d,]+)/gi);
				if (hotelPrices) {
					hotelPrices.forEach(m => {
						const parts = m.match(/([A-Z].+?)\s*(?:Total:|US\$|Precio)[\s:]*/i);
						if (parts && parts[1]) {
							const name = parts[1].trim();
							if (!seen.has(name)) {
								seen.add(name);
								hotels.push({ name, price: 'N/A' });
							}
						}
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
				hotels: hotels.slice(0, 5),
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
	session.Progress.SetCurrentAction(message)
	session.Progress.Timestamp = time.Now()

	// Calcular tiempo transcurrido
	elapsed := time.Since(session.StartTime)
	elapsedStr := elapsed.Round(time.Second).String()
	session.Progress.SetElapsedTime(elapsedStr)

	// Calcular velocidad estimada (basado en progreso completado)
	if elapsed.Seconds() > 0 && percent > 0 {
		itemsPerSec := (percent / 100) / elapsed.Seconds()
		session.Progress.SetSpeed(fmt.Sprintf("%.2f", itemsPerSec))

		// Calcular ETA
		if percent < 100 {
			remainingPercent := 100 - percent
			remainingTime := time.Duration((remainingPercent/100)/itemsPerSec) * time.Second
			session.Progress.SetETA(remainingTime.Round(time.Second).String())
		}
	}

	// Agregar información del hotel si está disponible
	if session.Order != nil && session.Order.HotelName != "" {
		session.Progress.SetCurrentHotel(session.Order.HotelName)
	}

	session.Progress.SetStatus("running")
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

// GetAllSessions returns all sessions (for getting all results)
func (s *ScraperService) GetAllSessions() []*ScraperSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sessions []*ScraperSession
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	return sessions
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

// Submit2FA delivers the 2FA code supplied by the user (via the web UI) to the
// scraping goroutine that is currently blocked waiting for it. Returns an error
// when no session is in waiting_2fa state or when the channel is no longer able
// to receive (e.g. the session was cancelled or already resumed).
func (s *ScraperService) Submit2FA(sessionID, code string) error {
	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found")
	}

	if session.Status != models.SessionStatusWaiting2FA {
		return fmt.Errorf("session is not waiting for a 2FA code (status: %s)", session.Status)
	}

	select {
	case session.TwoFACode <- code:
		return nil
	default:
		return fmt.Errorf("2FA code channel is not ready to receive")
	}
}

// Needs2FA reports whether the session is currently paused waiting for a 2FA
// code from the user. The web UI uses this to show/hide the 2FA input.
func (s *ScraperService) Needs2FA(sessionID string) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[sessionID]
	if !exists {
		return false, ""
	}
	return session.Status == models.SessionStatusWaiting2FA, session.Progress.CurrentAction
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

// runScrapingByID is the bridge called by the delfos provider's
// LegacyRunner when no order is supplied. It looks up the session by id
// and invokes the existing runScraping function.
func (s *ScraperService) runScrapingByID(sessionID string, cfg *config.LoginConfig) error {
	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()
	if !exists {
		return fmt.Errorf("scraper service: session %q not found", sessionID)
	}
	s.runScraping(session, cfg)
	return nil
}

// runScrapingWithOrderByID is the bridge called by the delfos provider
// when an order is supplied. It mirrors runScrapingByID but routes to
// runScrapingWithOrder so the existing delfos logic stays untouched.
func (s *ScraperService) runScrapingWithOrderByID(sessionID string, cfg *config.LoginConfig, order *api.ScrapingOrder) error {
	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()
	if !exists {
		return fmt.Errorf("scraper service: session %q not found", sessionID)
	}
	s.runScrapingWithOrder(session, cfg, order)
	return nil
}

// dispatchToProvider runs the registered provider for the given order.
// It is the non-delfos entry point that the StartSessionWithOrder
// delegates to when order.Provider != "delfos".
func (s *ScraperService) dispatchToProvider(sessionID string, order *api.ScrapingOrder) {
	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()
	if !exists {
		log.Printf("dispatchToProvider: session %q not found", sessionID)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("dispatchToProvider: panic in provider %q: %v", order.Provider, r)
			s.handleError(session, fmt.Errorf("provider %q panicked: %v", order.Provider, r))
		}
	}()

	cfg, err := config.LoadLoginConfig()
	if err != nil {
		s.handleError(session, fmt.Errorf("loading config: %w", err))
		return
	}

	// Override credentials if the order's provider ships its own. The
	// tip provider has its own LoadConfig that reads TIP_* variables,
	// but we also allow per-session overrides via the registry factory.
	factory, err := scrapers.Lookup(order.Provider)
	if err != nil {
		s.handleError(session, err)
		return
	}

	provider, err := factory(cfg)
	if err != nil {
		s.handleError(session, fmt.Errorf("building provider %q: %w", order.Provider, err))
		return
	}

	log.Printf("dispatchToProvider: session=%s provider=%s order=%s hotel=%s",
		sessionID, provider.Name(), order.ID, order.HotelName)

	providerSession := &scrapers.ScraperSession{
		ID:                  session.ID,
		Order:               order,
		BookedPrice:         order.BookedPrice,
		Ctx:                 session.Ctx,
		Cancel:              session.Cancel,
		TwoFACode:           session.TwoFACode,
		TwoFAErr:            session.TwoFAErr,
		TwoFAInputSelector:  session.TwoFAInputSelector,
		TwoFASubmitSelector: session.TwoFASubmitSelector,
		NewBrowser: func(parent context.Context) (context.Context, context.CancelFunc) {
			return s.pool.NewContext(parent)
		},
		Reporter: &serviceReporter{s: s, session: session},
	}

	if err := provider.Run(session.Ctx, providerSession, order); err != nil {
		s.handleError(session, fmt.Errorf("provider %q failed: %w", order.Provider, err))
		return
	}

	session.Status = models.SessionStatusCompleted
	session.Progress.SetStage(models.StageComplete)
	session.Progress.SetProgress(100)
	session.Progress.SetStatus("completed")
	now := time.Now()
	session.EndTime = &now
	ws.SendToSession(session.ID, session.Progress)
}

// serviceReporter adapts the ScraperService to the scrapers.Reporter
// interface so providers can publish progress without depending on the
// web service layer.
type serviceReporter struct {
	s       *ScraperService
	session *ScraperSession
}

func (r *serviceReporter) Progress(percent float64, stage, message string) {
	r.s.sendProgress(r.session, percent, stage, message)
}

func (r *serviceReporter) Error(err error) {
	r.s.handleError(r.session, err)
}

func (r *serviceReporter) Result(id string, data map[string]interface{}) {
	r.session.Results = append(r.session.Results, models.NewResult(id, data))
}

func (r *serviceReporter) Needs2FA(inputSel, submitSel string) bool {
	r.session.TwoFAInputSelector = inputSel
	r.session.TwoFASubmitSelector = submitSel
	r.session.Status = models.SessionStatusWaiting2FA
	r.session.Progress.SetStage(models.StageTwoFA)
	r.session.Progress.SetCurrentAction("Esperando código 2FA")
	ws.SendToSession(r.session.ID, r.session.Progress)
	return true
}

func (r *serviceReporter) Submit2FA(code string) error {
	return r.s.Submit2FA(r.session.ID, code)
}

var (
	debugMode = flag.Bool("debug", false, "Run in debug mode")
)

func init() {
	flag.Parse()
}
