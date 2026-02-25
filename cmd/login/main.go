package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ExpeditusClient/internal/browser"
	"ExpeditusClient/internal/config"

	"github.com/chromedp/chromedp"
)

const (
	searchURL      = "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&departureDate=09/05/2026&arrivalDate=23/05/2026&hotelDestination=Destination::AUA"
	defaultTimeout = 60 * time.Second
)

type LoginResult struct {
	SessionID string
	URL       string
	HotelName string
	Price     string
	Debug     string
}

func main() {
	debug := flag.Bool("debug", false, "Run in debug mode to analyze page structure")
	flag.Parse()

	ctx := context.Background()

	loginCfg, err := config.LoadLoginConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cfg := browser.DefaultConfig()
	cfg.Timeout = defaultTimeout
	cfg.Headless = !*debug // Headless unless debug mode

	pool, err := browser.NewPool(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating browser pool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *debug {
		runDebug(ctx, pool, loginCfg.TargetURL)
		return
	}

	result, err := runLogin(ctx, pool, loginCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	printResult(result)
}

// runLogin executes the login flow using semantic anchors
func runLogin(ctx context.Context, pool *browser.Pool, loginCfg *config.LoginConfig) (*LoginResult, error) {
	browserCtx, cancel := pool.NewContext(ctx)
	defer cancel()

	var sessionID, currentURL string
	var result map[string]interface{}
	var debugLog string

	// 1. Navigate to page and wait for load
	err := chromedp.Run(browserCtx,
		chromedp.Navigate(loginCfg.TargetURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("navigation failed: %w", err)
	}

	// 2. Click "Entrar" button - ANCLA: texto exacto "Entrar"
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			// ANCLA: Buscar por texto exacto "Entrar" 
			const entrarLink = document.getElementById('openLogin');
			if (entrarLink) {
				entrarLink.click();
				return 'clicked-openLogin';
			}
			// Fallback: buscar por texto
			const links = document.querySelectorAll('a, button');
			for (const link of links) {
				if (link.textContent?.trim().toLowerCase() === 'entrar') {
					link.click();
					return 'clicked-by-text';
				}
			}
			return 'not-found';
		})()`, nil),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("click entrar failed: %w", err)
	}

	// 3. Fill login form using ANCHOR-BASED approach
	// Strategy: Find inputs by proximity to text labels (robust against ID changes)
	fillScript := fmt.Sprintf(`(() => {
		const debug = [];
		
		// ANCLA: Primero encontrar el FORMULARIO de login
		// Buscar formulario que contenga input[type="password"]
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
		
		// Si no encontramos formulario, usar todo el documento
		const searchRoot = loginForm || document;
		
		let emailInput = null;
		let passwordInput = null;
		let submitButton = null;
		
		// ANCLA: Buscar password primero (es más específico)
		const passwordInputs = searchRoot.querySelectorAll('input[type="password"]');
		if (passwordInputs.length > 0) {
			passwordInput = passwordInputs[0];
			debug.push('Found password: ' + passwordInput.id);
		}
		
		// ANCLA: Buscar email por ID o nombre que contenga "Email" o "email"
		const textInputs = searchRoot.querySelectorAll('input[type="text"]');
		for (const input of textInputs) {
			const id = (input.id || '').toLowerCase();
			const name = (input.name || '').toLowerCase();
			const placeholder = (input.placeholder || '').toLowerCase();
			
			// Buscar el que tenga ID/nombre con "Email"
			if (id.includes('email') || name.includes('email')) {
				emailInput = input;
				debug.push('Found email by id/name: ' + input.id);
				break;
			}
		}
		
		// Si no encontró por ID, usar el que tiene placeholder "..." (pero solo del form de login)
		if (!emailInput && loginForm) {
			for (const input of textInputs) {
				const placeholder = (input.placeholder || '').toLowerCase();
				const id = (input.id || '').toLowerCase();
				if (placeholder === '...' && id.includes('login')) {
					emailInput = input;
					debug.push('Found email by placeholder in login form: ' + input.id);
					break;
				}
			}
		}
		
		// ANCLA: Buscar botón de submit por texto dentro del formulario
		// Primero buscar botón con texto "Iniciar"
		const allButtons = searchRoot.querySelectorAll('button, input[type="submit"], [role="button"]');
		for (const btn of allButtons) {
			const text = (btn.textContent || btn.value || '').toLowerCase().trim();
			// Buscar exactamente "Iniciar" o que contenga "iniciar" y esté en el formulario de login
			if (text === 'iniciar sesión' || text === 'iniciar') {
				submitButton = btn;
				debug.push('Found submit button: ' + text + ' tag:' + btn.tagName);
				break;
			}
		}
		
		// Si no encontró, buscar cualquier botón submit
		if (!submitButton) {
			for (const btn of allButtons) {
				const type = (btn.type || '').toLowerCase();
				if (type === 'submit') {
					submitButton = btn;
					debug.push('Found submit button by type: ' + btn.tagName);
					break;
				}
			}
		}
		
		// Llenar los campos
		if (emailInput) {
			emailInput.value = '%s';
			emailInput.dispatchEvent(new Event('input', { bubbles: true }));
			emailInput.dispatchEvent(new Event('change', { bubbles: true }));
			debug.push('Filled email OK');
		} else {
			debug.push('ERROR: Email not found');
		}
		
		if (passwordInput) {
			passwordInput.value = '%s';
			passwordInput.dispatchEvent(new Event('input', { bubbles: true }));
			passwordInput.dispatchEvent(new Event('change', { bubbles: true }));
			debug.push('Filled password OK');
		} else {
			debug.push('ERROR: Password not found');
		}
		
		// Click submit BUTTON (not form.submit()) - esto es importante para JSF
		if (submitButton) {
			submitButton.click();
			debug.push('Clicked submit button');
		} else if (loginForm) {
			// Solo como último recurso
			loginForm.submit();
			debug.push('Submitted form (last resort)');
		} else {
			debug.push('ERROR: No submit method');
		}
		
		return debug.join(' | ');
	})()`, loginCfg.Username, loginCfg.Password)

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(fillScript, &debugLog),
		chromedp.Sleep(4*time.Second),
		// Check URL after login attempt
		chromedp.Location(&currentURL),
		chromedp.Evaluate(`document.cookie.match(/JSESSIONID=([^;]+)/)?.[1] || ''`, &sessionID),
		// Get page content for debugging
		chromedp.Evaluate(`(() => {
			return {
				url: window.location.href,
				title: document.title,
				hasError: document.body.textContent.includes('incorrecta') || 
					document.body.textContent.includes('inválido') ||
					document.body.textContent.includes('error'),
				bodyText: document.body.innerText.substring(0, 500)
			};
		})()`, &result),
	)
	if err != nil {
		return nil, fmt.Errorf("login fill failed: %w", err)
	}

	// 4. Navigate to search URL
	err = chromedp.Run(browserCtx,
		chromedp.Navigate(searchURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.Location(&currentURL),
	)
	if err != nil {
		return nil, fmt.Errorf("search navigation failed: %w", err)
	}

	// 5. Extract hotel data and cookies
	extractScript := buildExtractScript()
	var extractResult map[string]interface{}
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(extractScript, &extractResult),
	)
	if err != nil {
		return nil, fmt.Errorf("extraction failed: %w", err)
	}

	// Get session info
	if cookies, ok := extractResult["cookies"].(string); ok {
		// Try to extract session ID from cookies
		if match := extractSessionFromCookies(cookies); match != "" {
			sessionID = match
		}
	}

	return parseResult(sessionID, currentURL, extractResult, debugLog), nil
}

func extractSessionFromCookies(cookies string) string {
	// Look for common session cookie names
	sessionNames := []string{"JSESSIONID", "SESSIONID", "JSESSIONID_SSO", "PHPSESSID", "ASP.NET_SessionId"}
	for _, name := range sessionNames {
		for _, cookie := range strings.Split(cookies, ";") {
			if strings.TrimSpace(cookie) == "" {
				continue
			}
			parts := strings.SplitN(cookie, "=", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[0]) == name {
				return parts[1]
			}
		}
	}
	return ""
}

// runDebug analyzes the page structure for development
func runDebug(ctx context.Context, pool *browser.Pool, url string) {
	browserCtx, cancel := pool.NewContext(ctx)
	defer cancel()

	var pageStruct map[string]interface{}

	err := chromedp.Run(browserCtx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.Evaluate(buildDebugScript(), &pageStruct),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Debug failed: %v\n", err)
		os.Exit(1)
	}

	// Print nicely formatted
	data, _ := json.MarshalIndent(pageStruct, "", "  ")
	fmt.Println(string(data))
}

func buildDebugScript() string {
	return `(() => {
		// First click "Entrar" to open modal
		const entrarLink = document.getElementById('openLogin');
		if (entrarLink) {
			entrarLink.click();
		}
		
		// Wait for modal to appear (synchronous wait)
		let attempts = 0;
		while (attempts < 10) {
			const modal = document.querySelector('[id*="login"]:not(#openLogin), [class*="modal"]:not(.hidden)');
			if (modal || attempts > 3) break;
			attempts++;
		}
		
		const result = {
			url: window.location.href,
			title: document.title,
			forms: [],
			buttons: [],
			inputs: [],
			modals: [],
			login_form: null
		};

		// Look for modals
		document.querySelectorAll('[class*="modal"], [id*="modal"], [id*="login"]').forEach(el => {
			const text = el.textContent?.trim();
			if (text && text.length > 10) {
				result.modals.push({
					tag: el.tagName,
					id: el.id,
					class: el.className,
					visible: el.offsetParent !== null
				});
			}
		});

		// Extract all forms
		document.querySelectorAll('form').forEach((form, i) => {
			const inputs = [];
			form.querySelectorAll('input, button').forEach(el => {
				inputs.push({
					tag: el.tagName,
					type: el.type,
					id: el.id,
					name: el.name,
					placeholder: el.placeholder,
					value: el.value
				});
			});
			result.forms.push({ index: i, action: form.action, inputs: inputs });
		});

		// Extract buttons with their text
		document.querySelectorAll('button, a, [role="button"]').forEach((el, i) => {
			const text = el.textContent?.trim();
			if (text && text.length > 0 && text.length < 30) {
				result.buttons.push({
					index: i,
					text: text,
					tag: el.tagName,
					id: el.id,
					class: el.className
				});
			}
		});

		// Extract all inputs
		document.querySelectorAll('input').forEach((el, i) => {
			result.inputs.push({
				index: i,
				type: el.type,
				id: el.id,
				name: el.name,
				placeholder: el.placeholder,
				value: el.value
			});
		});

		// Look for login form specifically - by input type and proximity
		document.querySelectorAll('form').forEach(form => {
			const action = form.action || '';
			const hasPassword = form.querySelector('input[type="password"]') !== null;
			const hasEmail = form.querySelector('input[type="email"]') !== null || 
				Array.from(form.querySelectorAll('input')).some(i => (i.id || '').toLowerCase().includes('email'));
			
			if (hasPassword || hasEmail || action.toLowerCase().includes('login')) {
				result.login_form = {
					action: action,
					inputs: Array.from(form.querySelectorAll('input')).map(el => ({
						type: el.type,
						id: el.id,
						name: el.name,
						placeholder: el.placeholder,
						value: el.value
					}))
				};
			}
		});

		return result;
	})()`
}

func buildExtractScript() string {
	return `(() => {
		try {
			// Get all cookies first
			const cookies = document.cookie;
			
			// Try multiple selectors for hotels
			const selectors = [
				'.hotel-card', '.hotel-result', '.accommodation-card',
				'[class*="hotel"]', '[class*="result"]', '[class*="accommodation"]',
				'.item', '.result-item', '.list-item'
			];
			
			let hotels = [];
			for (const sel of selectors) {
				hotels = document.querySelectorAll(sel);
				if (hotels.length > 0) {
					break;
				}
			}
			
			if (hotels.length === 0) {
				// Fallback: extract from page text
				const body = document.body ? document.body.innerText : '';
				return { 
					cookies: cookies,
					name: 'Page content', 
					price: body.substring(0, 300),
					hotelsCount: 0
				};
			}
			
			const results = [];
			for (const hotel of hotels) {
				const nameEl = hotel.querySelector('h1, h2, h3, h4, [class*="name"], [class*="title"]');
				const priceEl = hotel.querySelector('[class*="price"], [class*="amount"], .price');
				const name = nameEl ? nameEl.textContent?.trim() : null;
				const price = priceEl ? priceEl.textContent?.trim() : null;
				if (name || price) {
					results.push({ name: name || 'Unknown', price: price || 'Unknown' });
				}
			}
			
			if (results.length > 0) {
				return { 
					cookies: cookies,
					name: results[0].name, 
					price: results[0].price,
					hotelsCount: results.length
				};
			}
			
			// Last resort: look for any price on page
			const prices = document.querySelectorAll('[class*="price"], .amount, .value, span');
			const names = document.querySelectorAll('h1, h2, h3, h4');
			
			return { 
				cookies: cookies,
				name: names.length > 0 ? names[0].textContent?.trim() : 'Not found', 
				price: prices.length > 0 ? prices[0].textContent?.trim() : 'Not found',
				hotelsCount: hotels.length
			};
		} catch(e) {
			return { 
				cookies: '',
				name: 'Error: ' + e.message, 
				price: 'N/A',
				hotelsCount: 0
			};
		}
	})()`
}

func parseResult(sessionID, url string, raw map[string]interface{}, debug string) *LoginResult {
	result := &LoginResult{
		URL:   url,
		Debug: debug,
	}

	// If we're on the results page (not login page), consider it successful
	if !strings.Contains(url, "login.xhtml") && !strings.Contains(url, "login?") {
		result.SessionID = "LOGGED_IN"
	}

	if name, ok := raw["name"].(string); ok {
		result.HotelName = name
	}
	if price, ok := raw["price"].(string); ok {
		result.Price = price
	}

	// Debug: show cookies if present
	if cookies, ok := raw["cookies"].(string); ok && cookies != "" {
		result.Debug += " | Cookies: " + strings.Split(cookies, ";")[0] + "..."
	}

	return result
}

func printResult(r *LoginResult) {
	fmt.Println("=== RESULTADO ===")
	if r.SessionID != "" {
		fmt.Printf("Session ID: %s\n", r.SessionID)
	} else {
		fmt.Println("Session ID: (no obtenido)")
	}
	fmt.Printf("URL: %s\n", r.URL)
	fmt.Printf("Hotel: %s\n", r.HotelName)
	fmt.Printf("Precio: %s\n", r.Price)
	if r.Debug != "" {
		fmt.Printf("Debug: %s\n", r.Debug)
	}
}
