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
	hotelUrl           = "https://www.delfos.tur.ar/accommodation/79731/available/1?tripId=3"
	searchURL          = "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&&departureDate=09/05/2026&arrivalDate=23/05/2026&hotelDestination=Destination::AUA"
	accommodationValue = "Barcelo Aruba (id: 79731)"
	roomType           = "Habitación Deluxe (vista al mar)"
	roomMealPlan       = "TODO INCLUIDO"
	roomPriceMax       = 10000
	roomPrice          = ""
	defaultTimeout     = 120 * time.Second
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

	log.Printf("Session %s: Waiting for 'Búsqueda completada'...", session.ID)
	var searchComplete bool
	for i := 0; i < 30; i++ {
		err = chromedp.Run(browserCtx,
			chromedp.Evaluate(`document.body.innerText.includes('Búsqueda completada')`, &searchComplete),
		)
		if err == nil && searchComplete {
			log.Printf("Session %s: Búsqueda completada found!", session.ID)
			break
		}
		time.Sleep(1 * time.Second)
	}

	time.Sleep(500 * time.Millisecond)

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

	fillValueScript := fmt.Sprintf(`(() => {
		const inputs = document.querySelectorAll('input');
		
		const fullValue = '%s';
		const idMatch = fullValue.match(/\(id:\s*(\d+)\)/);
		const hotelId = idMatch ? idMatch[1] : null;
		const searchName = fullValue.replace(/\s*\(id:\s*\d+\)/, '').trim();
		
		for (const input of inputs) {
			const placeholder = (input.placeholder || '').toLowerCase();
			const ariaLabel = (input.getAttribute('aria-label') || '').toLowerCase();
			const name = (input.name || '').toLowerCase();
			
			if (placeholder.includes('nombre del alojamiento') || 
				ariaLabel.includes('nombre del alojamiento') ||
				name.includes('accommodation') ||
				placeholder.includes('alojamiento')) {
				
				input.value = '';
				
				for (let i = 0; i < searchName.length; i++) {
					input.value += searchName[i];
					input.dispatchEvent(new Event('input', { bubbles: true }));
					input.dispatchEvent(new Event('keyup', { bubbles: true }));
				}
				
				input.dispatchEvent(new Event('input', { bubbles: true }));
				input.dispatchEvent(new Event('change', { bubbles: true }));
				input.dispatchEvent(new Event('blur', { bubbles: true }));
				
				setTimeout(() => {
					let attempts = 0;
					while (attempts < 10) {
						const loader = document.querySelector('[class*="loader"], [class*="loading"], [class*="spinner"]');
						if (!loader || loader.offsetParent === null) break;
						attempts++;
					}
					
					if (hotelId) {
						const hotelElements = document.querySelectorAll('[id*="datascrollHorizontal"][id*=":hotelextended"]');
						for (const el of hotelElements) {
							const elId = el.id || '';
							if (elId.includes(':' + hotelId + ':') || elId.endsWith(':' + hotelId)) {
								const links = el.querySelectorAll('a, button');
								for (const link of links) {
									const linkText = (link.textContent || '').toLowerCase();
									if (linkText.includes('ver opciones') || linkText.includes('ver hotel')) {
										const url = link.href || link.getAttribute('data-url') || link.getAttribute('onclick');
										return 'URL_FOUND: ' + url;
									}
								}
							}
						}
					}
					
					return 'NO URL FOUND';
				}, 3000);
				
				return 'TYPED: ' + searchName + ' | ID: ' + hotelId;
			}
		}
		
		return 'NOT_FOUND';
	})()`, accommodationValue)

	var fillValueResult string
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(fillValueScript, &fillValueResult),
	)
	if err != nil {
		log.Printf("Session %s: Error filling: %v", session.ID, err)
	}

	log.Printf("Session %s: Fill result: [%s]", session.ID, fillValueResult)

	time.Sleep(5 * time.Second)

	var clickResult string
	clickHotelScript := fmt.Sprintf(`(() => {
		const fullValue = '%s';
		const match = fullValue.match(/\(id:\s*(\d+)\)/);
		const hotelId = match ? match[1] : null;
		
		if (!hotelId) return 'NO_ID: ' + fullValue;
		
		const allLinks = document.querySelectorAll('a[href*="/' + hotelId + '/"]');
		for (const link of allLinks) {
			const linkText = (link.textContent || '').toLowerCase().trim();
			const href = link.href;
			if (href && href.includes('/' + hotelId + '/') && (linkText.includes('ver opciones') || linkText.includes('ver hotel'))) {
				window.location.href = href;
				return 'NAVIGATING: ' + href;
			}
		}
		
		const openHotelLinks = document.querySelectorAll('[id*="openHotel"]');
		for (const link of openHotelLinks) {
			const href = link.href || '';
			if (href.includes('/' + hotelId + '/')) {
				const linkText = (link.textContent || '').toLowerCase().trim();
				window.location.href = href;
				return 'NAVIGATING (openHotel): ' + href;
			}
		}
		
		const debugLinks = Array.from(allLinks).map(l => l.href).join(', ');
		return 'NOT_FOUND - hotelId: ' + hotelId + ' | links found: ' + debugLinks;
	})()`, accommodationValue)

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(clickHotelScript, &clickResult),
	)
	if err != nil {
		log.Printf("Session %s: Error clicking hotel: %v", session.ID, err)
	}

	log.Printf("Session %s: Click result: [%s]", session.ID, clickResult)

	log.Printf("Session %s: Waiting for rooms to load...", session.ID)
	var roomsLoaded map[string]interface{}
	for i := 0; i < 30; i++ {
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
				log.Printf("Session %s: Rooms loaded!", session.ID)
				break
			}
		}
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
		
		return 'UNKNOWN PAGE - url: ' + url;
	})()`, accommodationValue)

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(checkHotelScript, &hotelPageCheck),
	)
	if err != nil {
		log.Printf("Session %s: Error checking hotel: %v", session.ID, err)
	}

	log.Printf("Session %s: Hotel page check: [%s]", session.ID, hotelPageCheck)

	time.Sleep(10 * time.Second)

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

	time.Sleep(5 * time.Second)

	hotelData := s.extractCurrentHotel(browserCtx, session)

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

func (s *ScraperService) extractCurrentHotel(ctx context.Context, session *ScraperSession) map[string]interface{} {
	var result map[string]interface{}
	script := `(() => {
		const url = window.location.href;
		const bodyText = document.body.innerText;
		
		const hotelEl = document.getElementById('hotel');
		const nameEl = hotelEl ? hotelEl.querySelector('.hotel-name') : null;
		const hotelName = nameEl ? nameEl.innerText.trim() : 'Not found';
		
		const bookOptionsDiv = document.getElementById('booking-options:bookOptions');
		
		const roomMap = {};
		if (bookOptionsDiv) {
			const combinations = bookOptionsDiv.querySelectorAll('.hotelCombinationPanel');
			
			combinations.forEach((el, i) => {
				const roomNameEl = el.querySelector('.dev-room');
				const roomName = roomNameEl ? roomNameEl.innerText.trim() : '';
				
				const mealPlanEl = el.querySelector('.dev-mealplan');
				const mealPlan = mealPlanEl ? mealPlanEl.innerText.trim() : '';
				
				const priceEl = el.querySelector('.dev-combination-price') || el.querySelector('.dev-combination-row-details');
				const priceText = priceEl ? priceEl.innerText.trim() : '';
				
				const currencyMatch = priceText.match(/US?\$/);
				const currency = currencyMatch ? currencyMatch[0] : '$';
				
				const numericPriceMatch = priceText.replace(/[^0-9]/g, '');
				const price = numericPriceMatch ? parseInt(numericPriceMatch, 10) : 0;
				
				const cancellationEl = el.querySelector('.freecancellation, .cancellation-policy');
				const cancellation = cancellationEl ? cancellationEl.innerText.trim() : '';
				
				const cancellationSummaryEl = el.querySelector('[class*="cancellation"], .cancellation-summary, .freecancellation-text');
				const cancellationSummary = cancellationSummaryEl ? cancellationSummaryEl.innerText.trim() : '';
				
				if (roomName && mealPlan) {
					if (!roomMap[roomName]) {
						roomMap[roomName] = { room: roomName, mealPlans: [] };
					}
					roomMap[roomName].mealPlans.push({
						plan: mealPlan,
						price: price,
						currency: currency,
						cancellation: cancellation,
						cancellationSummary: cancellationSummary
					});
				}
			});
		}
		
		const rooms = Object.values(roomMap);
		
		const roomType = 'Habitación Deluxe (vista al mar)';
		const roomMealPlan = 'TODO INCLUIDO';
		
		const filteredRooms = rooms.filter(r => {
			const matchesRoomType = r.room.toLowerCase().includes(roomType.toLowerCase());
			const matchesMealPlan = r.mealPlans.some(mp => 
				mp.plan.toLowerCase().includes(roomMealPlan.toLowerCase())
			);
			return matchesRoomType && matchesMealPlan;
		});
		
		const debug = {
			url: url,
			hotelName: hotelName,
			roomCount: filteredRooms.length,
			maxPrice: 10000,
			room: filteredRooms.length > 0 ? {
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

	log.Printf("Session %s: Current hotel: %s, Rooms found: %v", session.ID, result["hotelName"], result["roomCount"])
	return result
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
					if (lower.includes('hotel') || lower.includes('aruba') || lower.includes('inn') || 
						lower.includes('resort') || lower.includes('village') || lower.includes('boutique') ||
						lower.includes('place') || lower.includes('manor') || lower.includes('beach') ||
						lower.includes('park')) {
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
