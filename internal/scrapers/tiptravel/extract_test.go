package tiptravel

import (
	"strings"
	"testing"

	"ExpeditusClient/internal/api"
)

const sampleSearchHTML = `<html><body>
<div class="c-card">
  <h3>Río de Janeiro - 5 noches</h3>
  <b class="c-card__idea-price__primary">US$1,068</b>
</div>
<div class="c-card">
  <h3>Florianópolis</h3>
  <b class="c-card__idea-price__primary">US$656</b>
</div>
<div class="c-card">
  <h3>Camboriú con vuelo</h3>
  <b class="c-card__idea-price__primary">US$5,235</b>
</div>
<div class="footer">
  <span>Cambio dolar $1530</span>
</div>
<div class="card-with-no-price">
  <p>Sin precio disponible</p>
</div>
</body></html>`

func TestExtractPricesFromHTML(t *testing.T) {
	p := New(nil)
	order := &api.ScrapingOrder{
		ID:          "test-order",
		HotelName:   "Royalton Riviera Cancun",
		HotelID:     "MASTER-97165",
		BookedPrice: 770.21,
	}

	results := p.extractPricesFromHTML(sampleSearchHTML, order)

	// Expect 3 card prices + 1 search_page marker
	count := 0
	for id, r := range results {
		if id == "search_page" {
			continue
		}
		count++
		if _, ok := r["price"].(float64); !ok {
			t.Errorf("%s missing price field: %+v", id, r)
		}
	}
	if count != 3 {
		t.Errorf("expected 3 card prices, got %d", count)
	}

	// search_page marker
	sp, ok := results["search_page"]
	if !ok {
		t.Fatal("missing search_page marker")
	}
	pricesFound, _ := sp["prices_found"].(int)
	if pricesFound != 3 {
		t.Errorf("search_page.prices_found = %d, want 3", pricesFound)
	}

	// Verify price values match what's in the HTML
	expectedPrices := map[float64]bool{1068: false, 656: false, 5235: false}
	for _, r := range results {
		if p, ok := r["price"].(float64); ok {
			if _, want := expectedPrices[p]; want {
				expectedPrices[p] = true
			}
		}
	}
	for price, found := range expectedPrices {
		if !found {
			t.Errorf("expected price %v not found", price)
		}
	}
}

func TestExtractPricesEmptyHTML(t *testing.T) {
	p := New(nil)
	order := &api.ScrapingOrder{ID: "x", HotelName: "x", HotelID: "x"}
	results := p.extractPricesFromHTML("", order)
	if len(results) != 1 {
		t.Errorf("expected 1 result (search_page marker), got %d", len(results))
	}
	if _, ok := results["search_page"]; !ok {
		t.Error("expected search_page marker")
	}
}

func TestExtractPricesFiltersSmallNumbers(t *testing.T) {
	p := New(nil)
	order := &api.ScrapingOrder{ID: "x", HotelName: "x", HotelID: "x"}
	html := `<b class="c-card__idea-price__primary">US$50</b>` // 50 < 100 threshold
	results := p.extractPricesFromHTML(html, order)
	for id := range results {
		if id != "search_page" {
			t.Errorf("expected only search_page marker, got %s", id)
		}
	}
}

func TestExtractPricesHotelIDDirectPage(t *testing.T) {
	// The "URL directa" style page renders the single hotel page with
	// MASTER-XXX visible. The extractor's strategy 2 grabs the price
	// near the hotel id.
	p := New(nil)
	order := &api.ScrapingOrder{
		ID:        "x",
		HotelID:   "MASTER-97165",
		HotelName: "Royalton Riviera Cancun",
		RoomType:  "1 Luxury junior suite",
		MealPlan:  "TODO INCLUIDO",
	}
	html := strings.Repeat("x", 500) + "MASTER-97165" + ` price here: US$770</body>`
	results := p.extractPricesFromHTML(html, order)
	if _, ok := results["direct"]; !ok {
		t.Errorf("expected direct result, got %v", results)
	}
}