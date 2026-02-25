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
	searchURL      = "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&&departureDate=09/05/2026&arrivalDate=23/05/2026&hotelDestination=Destination::AUA"
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

	cfg, err := config.LoadLoginConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	browserCfg := browser.DefaultConfig()
	browserCfg.Timeout = defaultTimeout
	browserCfg.Headless = !*debug

	pool, err := browser.NewPool(ctx, browserCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating browser pool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *debug {
		runDebug(ctx, pool, cfg.TargetURL)
		return
	}

	result, err := runLogin(ctx, pool, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	printResult(result)
}

func runLogin(ctx context.Context, pool *browser.Pool, cfg *config.LoginConfig) (*LoginResult, error) {
	browserCtx, cancel := pool.NewContext(ctx)
	defer cancel()

	var sessionID, currentURL string
	var debugLog string

	err := chromedp.Run(browserCtx,
		chromedp.Navigate(cfg.TargetURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("navigation failed: %w", err)
	}

	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(`(() => {
			// ANCHOR: Find "Entrar" by exact text match
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
		return nil, fmt.Errorf("click entrar failed: %w", err)
	}

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
			debug.push('Found password: ' + passwordInput.id);
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
					debug.push('Found email by placeholder in login form: ' + input.id);
					break;
				}
			}
		}
		
		const allButtons = searchRoot.querySelectorAll('button, input[type="submit"], [role="button"]');
		for (const btn of allButtons) {
			const text = (btn.textContent || btn.value || '').toLowerCase().trim();
			if (text === 'iniciar sesión' || text === 'iniciar') {
				submitButton = btn;
				debug.push('Found submit button: ' + text + ' tag:' + btn.tagName);
				break;
			}
		}
		
		if (!submitButton) {
			for (const btn of allButtons) {
				const btnType = (btn.type || '').toLowerCase();
				if (btnType === 'submit') {
					submitButton = btn;
					debug.push('Found submit button by type: ' + btn.tagName);
					break;
				}
			}
		}
		
		if (!emailInput) {
			debug.push('ERROR: Email not found');
		} else {
			emailInput.value = '%s';
			emailInput.dispatchEvent(new Event('input', { bubbles: true }));
			emailInput.dispatchEvent(new Event('change', { bubbles: true }));
			debug.push('Filled email OK');
		}
		
		if (!passwordInput) {
			debug.push('ERROR: Password not found');
		} else {
			passwordInput.value = '%s';
			passwordInput.dispatchEvent(new Event('input', { bubbles: true }));
			passwordInput.dispatchEvent(new Event('change', { bubbles: true }));
			debug.push('Filled password OK');
		}
		
		if (submitButton) {
			submitButton.click();
			debug.push('Clicked submit button');
		} else {
			if (loginForm) {
				loginForm.submit();
				debug.push('Submitted form (last resort)');
			} else {
				debug.push('ERROR: No submit method');
			}
		}
		
		return debug.join(' | ');
	})()`, cfg.Username, cfg.Password)

	var extractResult map[string]interface{}
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(fillScript, &debugLog),
		chromedp.Sleep(4*time.Second),
		chromedp.Location(&currentURL),
		chromedp.Evaluate(`document.cookie.match(/JSESSIONID=([^;]+)/)?.[1] || ''`, &sessionID),
		chromedp.Evaluate(`(() => {
			return {
				url: window.location.href,
				title: document.title,
				hasError: document.body.textContent.includes('incorrecta') || 
					document.body.textContent.includes('inválido') ||
					document.body.textContent.includes('error'),
				bodyText: document.body.innerText.substring(0, 500)
			};
		})()`, &extractResult),
	)
	if err != nil {
		return nil, fmt.Errorf("login fill failed: %w", err)
	}

	err = chromedp.Run(browserCtx,
		chromedp.Navigate(searchURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(5*time.Second),
		chromedp.Location(&currentURL),
	)
	if err != nil {
		return nil, fmt.Errorf("search navigation failed: %w", err)
	}

	// Wait for results and extract
	hotelData := extractHotels(browserCtx)
	return parseResult(sessionID, currentURL, hotelData, debugLog), nil
}

func extractHotels(ctx context.Context) map[string]interface{} {
	var result map[string]interface{}
	chromedp.Run(ctx,
		chromedp.Evaluate(buildExtractScript(), &result),
	)
	return result
}

func buildExtractScript() string {
	return `(() => {
		try {
			const cookies = document.cookie;
			const url = window.location.href;
			const allText = document.body.innerText;
			
			// ANCHOR: Find first price from "Total:" line (first hotel result)
			const firstTotalMatch = allText.match(/Total: US\$[\d,.]+/);
			let finalPrice = null;
			if (firstTotalMatch) {
				const priceInLine = firstTotalMatch[0].match(/US\$[\d,.]+/);
				finalPrice = priceInLine ? priceInLine[0] : null;
			}
			
			// ANCHOR: Find FIRST occurrence of any known hotel brand name
			// Look for specific known hotel names that come BEFORE or near the first price
			const knownHotels = [
				'Radisson Blu Aruba', 'Radisson Blu', 'Radisson',
				'Hilton Aruba', 'Hilton',
				'Marriott',
				'Casa del Mar', 'Casa del Mar Aruba',
				'Boardwalk', 'Boardwalk Aruba',
				'ECLIPSE', 'Divi Aruba', 'Divi',
				'Renaissance Aruba', 'Renaissance',
				'Embassy Suites', 'Embassy Aruba',
				'Holiday Inn Aruba', 'Holiday Inn',
				'Gold Coast', 'Tamarijn'
			];
			
			let hotelName = null;
			for (const hotel of knownHotels) {
				if (allText.includes(hotel)) {
					hotelName = hotel;
					break;
				}
			}
			
			// Fallback: look for any line with brand name
			if (!hotelName) {
				const lines = allText.split(/[\n\r]+/);
				const brandNames = ['radisson', 'hilton', 'marriott', 'hyatt', 'sheraton', 'barcelo', 'melia', 'eagle', 'paradise', 'victoria', 'imperial'];
				
				for (const line of lines) {
					const lower = line.toLowerCase();
					const hasBrand = brandNames.some(b => lower.includes(b));
					// Skip if it's a provider or category
					if (hasBrand && !lower.includes('group') && !lower.includes('beds') && 
						!lower.includes('choice') && !lower.includes('hotelbeds') && line.length > 6 && line.length < 50) {
						hotelName = line.trim();
						break;
					}
				}
			}
			
			// Fallback prices
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
}

func extractSessionFromCookies(cookies string) string {
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

	data, _ := json.MarshalIndent(pageStruct, "", "  ")
	fmt.Println(string(data))
}

func buildDebugScript() string {
	return `(() => {
		const entrarLink = document.getElementById('openLogin');
		if (entrarLink) {
			entrarLink.click();
		}
		
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

func parseResult(sessionID, url string, raw map[string]interface{}, debug string) *LoginResult {
	result := &LoginResult{
		URL:   url,
		Debug: debug,
	}

	if !strings.Contains(url, "login.xhtml") && !strings.Contains(url, "login?") {
		result.SessionID = "LOGGED_IN"
	}

	if name, ok := raw["name"].(string); ok {
		result.HotelName = name
	}
	if price, ok := raw["price"].(string); ok {
		result.Price = price
	}

	if cookies, ok := raw["cookies"].(string); ok && cookies != "" {
		result.Debug += " | Cookies: " + strings.Split(cookies, ";")[0] + "..."
	}

	return result
}

func printResult(r *LoginResult) {
	fmt.Println("=== RESULT ===")
	if r.SessionID == "" {
		fmt.Println("Session ID: (not obtained)")
		return
	}
	fmt.Printf("Session ID: %s\n", r.SessionID)
	fmt.Printf("URL: %s\n", r.URL)
	fmt.Printf("Hotel: %s\n", r.HotelName)
	fmt.Printf("Price: %s\n", r.Price)
	if r.Debug != "" {
		fmt.Printf("Debug: %s\n", r.Debug)
	}
}
