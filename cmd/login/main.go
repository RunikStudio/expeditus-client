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
	loginModalBtn  = "#openLogin"
	emailInput     = "#j_id_4s_3_1:login-content:login\\:Email"
	passwordInput  = "#j_id_4s_3_1:login-content:login\\:j_password"
	submitBtn      = "#j_id_4s_3_1:login-content:login\\:signin"
	defaultTimeout = 30 * time.Second
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

	loginScript := buildLoginScript(loginCfg.Username, loginCfg.Password)

	err := chromedp.Run(browserCtx,
		chromedp.Navigate(loginCfg.TargetURL),
		chromedp.WaitVisible(loginModalBtn, chromedp.ByQuery),
		chromedp.Click(loginModalBtn, chromedp.ByQuery),
		chromedp.WaitVisible(emailInput, chromedp.ByQuery),
		chromedp.SendKeys(emailInput, loginCfg.Username, chromedp.ByQuery),
		chromedp.SendKeys(passwordInput, loginCfg.Password, chromedp.ByQuery),
		chromedp.Click(submitBtn, chromedp.ByQuery),
		chromedp.WaitNotVisible(submitBtn, chromedp.ByQuery),
		chromedp.Evaluate(loginScript, &sessionID),
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
