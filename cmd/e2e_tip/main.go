// e2e_tip: real end-to-end test of the tiptravel scraper against
// tiptravelya.com. Run with:
//
//	cd ExpeditusClient && go run cmd/e2e_tip/main.go
//
// Reads credentials from ExpeditusApi/.env (userTip/passTip). Uses a
// real order from BASE HOTELES CSV (HOTEL-TTY-20475-1) as a sample.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"ExpeditusClient/internal/api"
	"ExpeditusClient/internal/browser"
	"ExpeditusClient/internal/scrapers"
	"ExpeditusClient/internal/scrapers/tiptravel"

	"github.com/chromedp/chromedp"
)

func main() {
	loadEnvFromAPI()

	cfg := tiptravel.LoadConfig()
	fmt.Printf("== tiptravel e2e ==\n")
	fmt.Printf("URL:      %s\n", cfg.LoginURL)
	fmt.Printf("Username: %s\n", cfg.Username)
	fmt.Printf("Password: %s (%d chars)\n\n", mask(cfg.Password), len(cfg.Password))

	if cfg.Password == "" {
		log.Fatal("missing passTip env var")
	}

	// Order from the CSV row HOTEL-TTY-20475-1 (Royalton Riviera Cancun).
	order := &api.ScrapingOrder{
		ID:          "HOTEL-TTY-20475-1",
		ServiceRef:  "TTY-20475",
		HotelName:   "Royalton Riviera Cancun",
		HotelID:     "MASTER-97165",
		RoomType:    "1 Luxury junior suite - LXUUJ",
		MealPlan:    "TODO INCLUIDO",
		BookedPrice: 770.21,
		Currency:    "USD",
		CheckIn:     "23/03/2026",
		CheckOut:    "26/03/2026",
		Passengers:  "2A",
		Provider:    scrapers.ProviderTipTravel,
		SearchURL:   cleanSearchURL("https://www.tiptravelya.com/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&distribution=2~~0~~\n\n::&departureDate=23/03/2026&arrivalDate=26/03/2026&hotelDestination=Destination::CUN"),
	}

	results := make(chan map[string]interface{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	prov := tiptravel.New(cfg)
	psession := &scrapers.ScraperSession{
		ID:          "e2e-1",
		Order:       order,
		BookedPrice: order.BookedPrice,
		Ctx:         ctx,
		Cancel:      cancel,
		NewBrowser: func(parent context.Context) (context.Context, context.CancelFunc) {
			bcfg := browser.DefaultConfig()
			bcfg.Headless = true
			bcfg.Timeout = 3 * time.Minute
			pool, err := browser.NewPool(parent, bcfg, 1)
			if err != nil {
				log.Fatalf("browser pool: %v", err)
			}
			return pool.NewContext(parent)
		},
		Reporter: &simpleReporter{results: results},
	}

	fmt.Println("Running provider (login + search + extract)...")
	start := time.Now()

	var runErr error
	go func() {
		runErr = prov.Run(ctx, psession, order)
		close(results)
	}()

	allResults := resultsAsSlice(results)
	elapsed := time.Since(start)

	if runErr != nil {
		fmt.Printf("\n[ERROR] %v (after %s)\n", runErr, elapsed)
	}

	fmt.Printf("\n== Results (%d items, %s) ==\n", len(allResults), elapsed)
	var prices []float64
	for _, r := range allResults {
		if pf, ok := r["prices_found"].(float64); ok {
			bp, _ := r["booked_price"].(float64)
			fmt.Printf("  [RESUMEN] %d precios extraídos | booked=%.2f USD\n", int(pf), bp)
			continue
		}
		if price, ok := r["price"].(float64); ok {
			prices = append(prices, price)
		}
	}
	if len(prices) > 0 {
		minP, maxP := prices[0], prices[0]
		for _, p := range prices[1:] {
			if p < minP {
				minP = p
			}
			if p > maxP {
				maxP = p
			}
		}
		fmt.Printf("  [RANGO] min=%.2f | max=%.2f | variación=%.2f (%.0f%%)\n",
			minP, maxP, maxP-minP, (maxP/minP-1)*100)
		fmt.Printf("\n  Todos los precios:\n")
		for _, p := range prices {
			fmt.Printf("    US$%.2f\n", p)
		}
	}
}

type simpleReporter struct {
	results chan map[string]interface{}
}

func (r *simpleReporter) Progress(p float64, stage, msg string) {
	fmt.Printf("  [%3.0f%%] %s: %s\n", p, stage, msg)
}

func (r *simpleReporter) Error(err error) {
	fmt.Printf("  [ERROR] %v\n", err)
}

func (r *simpleReporter) Result(id string, data map[string]interface{}) {
	r.results <- data
	fmt.Printf("  [RESULT] %s\n", id)
}

func (r *simpleReporter) Needs2FA(inputSel, submitSel string) bool {
	fmt.Printf("  [2FA] needed: input=%s submit=%s\n", inputSel, submitSel)
	return false
}

func (r *simpleReporter) Submit2FA(code string) error { return nil }

func mask(s string) string {
	if len(s) <= 2 {
		return "**"
	}
	return s[:2] + strings.Repeat("*", len(s)-2)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// cleanSearchURL strips whitespace and newlines from a URL that came
// from a CSV cell (Travel Compositor exports inject \n in the middle of
// distribution parameters).
func cleanSearchURL(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", "")
	return strings.TrimSpace(s)
}

func loadEnvFromAPI() {
	data, err := os.ReadFile("../ExpeditusApi/.env")
	if err != nil {
		fmt.Printf("warning: cannot read ../ExpeditusApi/.env: %v\n", err)
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, `"`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}

// Avoid the unused import warning on chromedp if the build strips it.
var _ = chromedp.Headless

func resultsAsSlice(ch chan map[string]interface{}) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, 32)
	for r := range ch {
		out = append(out, r)
	}
	return out
}

var _ = exec.Command