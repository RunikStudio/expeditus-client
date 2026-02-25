package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"ExpeditusClient/internal/browser"
	"ExpeditusClient/internal/config"

	"github.com/chromedp/chromedp"
)

const (
	searchURL      = "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&departureDate=09/05/2026&arrivalDate=23/05/2026&hotelDestination=Destination::AUA"
	loginModalBtn  = "button:contains('Entrar'), a:contains('Entrar'), [id*='login'], [class*='login']"
	emailInput     = "input[type='email'], input[id*='email'], input[id*='Email'], input[name*='email']"
	passwordInput  = "input[type='password'], input[id*='password'], input[id*='Password'], input[name*='password']"
	submitBtn      = "button[type='submit'], button:contains('Iniciar'), button:contains('Entrar'), input[type='submit']"
	defaultTimeout = 60 * time.Second
)

type LoginResult struct {
	SessionID string
	URL       string
	HotelName string
	Price     string
}

func main() {
	ctx := context.Background()

	loginCfg, err := config.LoadLoginConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cfg := browser.DefaultConfig()
	cfg.Timeout = defaultTimeout

	pool, err := browser.NewPool(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating browser pool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	result, err := runLogin(ctx, pool, loginCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	printResult(result)
}

func runLogin(ctx context.Context, pool *browser.Pool, loginCfg *config.LoginConfig) (*LoginResult, error) {
	browserCtx, cancel := pool.NewContext(ctx)
	defer cancel()

	var sessionID, currentURL string
	var result map[string]interface{}

	err := chromedp.Run(browserCtx,
		chromedp.Navigate(loginCfg.TargetURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.Evaluate(`(() => {
			const entrarBtn = Array.from(document.querySelectorAll('button, a')).find(el => el.textContent.includes('Entrar'));
			if (entrarBtn) { entrarBtn.click(); return 'clicked'; }
			return 'not found';
		})()`, nil),
		chromedp.Sleep(2*time.Second),
		chromedp.Evaluate(`(() => {
			const emailInput = document.querySelector('input[type="email"], input[id*="email" i], input[name*="email" i]');
			const passwordInput = document.querySelector('input[type="password"], input[id*="password" i], input[name*="password" i]');
			if (emailInput) emailInput.value = '`+loginCfg.Username+`';
			if (passwordInput) passwordInput.value = '`+loginCfg.Password+`';
			return (emailInput && passwordInput) ? 'filled' : 'inputs not found';
		})()`, nil),
		chromedp.Sleep(1*time.Second),
		chromedp.Evaluate(`(() => {
			const submitBtn = Array.from(document.querySelectorAll('button[type="submit"], button, input[type="submit"]')).find(el => el.textContent.includes('Iniciar') || el.textContent.includes('Entrar'));
			if (submitBtn) { submitBtn.click(); return 'clicked'; }
			const forms = document.querySelectorAll('form');
			if (forms.length > 0) { forms[0].submit(); return 'form submitted'; }
			return 'not found';
		})()`, nil),
		chromedp.Sleep(3*time.Second),
		chromedp.Evaluate(`document.cookie.match(/JSESSIONID=([^;]+)/)?.[1] || ''`, &sessionID),
	)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	err = chromedp.Run(browserCtx,
		chromedp.Navigate(searchURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Location(&currentURL),
	)
	if err != nil {
		return nil, fmt.Errorf("navigation failed: %w", err)
	}

	extractScript := buildExtractScript()
	err = chromedp.Run(browserCtx,
		chromedp.Evaluate(extractScript, &result),
	)
	if err != nil {
		return nil, fmt.Errorf("extraction failed: %w", err)
	}

	return parseResult(sessionID, currentURL, result), nil
}

func buildLoginScript(_, _ string) string {
	return `document.cookie.match(/JSESSIONID=([^;]+)/)?.[1] || ''`
}

func buildExtractScript() string {
	return `(() => {
		const hotels = document.querySelectorAll('[class*="hotel"], [class*="result"], [class*="accommodation"]');
		for (const hotel of hotels) {
			const nameEl = hotel.querySelector('h1, h2, h3, h4, [class*="name"], [class*="title"]');
			const priceEl = hotel.querySelector('[class*="price"]');
			if (nameEl && priceEl) {
				return { name: nameEl.textContent?.trim(), price: priceEl.textContent?.trim() };
			}
		}
		const allPrices = document.querySelectorAll('[class*="price"]');
		const allNames = document.querySelectorAll('h1, h2, h3, h4');
		if (allPrices.length > 0 && allNames.length > 0) {
			return { name: allNames[0].textContent?.trim(), price: allPrices[0].textContent?.trim() };
		}
		return { name: 'No encontrado', price: 'No encontrado' };
	})()`
}

func parseResult(sessionID, url string, raw map[string]interface{}) *LoginResult {
	result := &LoginResult{
		SessionID: sessionID,
		URL:       url,
	}

	if name, ok := raw["name"].(string); ok {
		result.HotelName = name
	}
	if price, ok := raw["price"].(string); ok {
		result.Price = price
	}

	return result
}

func printResult(r *LoginResult) {
	fmt.Println("=== RESULTADO ===")
	if r.SessionID != "" {
		fmt.Printf("Session ID: %s\n", r.SessionID)
	}
	fmt.Printf("URL: %s\n", r.URL)
	fmt.Printf("Hotel: %s\n", r.HotelName)
	fmt.Printf("Precio: %s\n", r.Price)
}
