package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"ExpeditusClient/internal/config"
)

// ScrapingOrder represents an order from the API
type ScrapingOrder struct {
	ID          string  `json:"id"`
	SearchURL   string  `json:"search_url"`
	HotelName   string  `json:"hotel_name"`
	HotelID     string  `json:"hotel_id"`
	RoomType    string  `json:"room_type"`
	MealPlan    string  `json:"meal_plan"`
	BookedPrice float64 `json:"booked_price"`
	Currency    string  `json:"currency"`
	CheckIn     string  `json:"check_in"`
	CheckOut    string  `json:"check_out"`
	Passengers  string  `json:"passengers"`
	ServiceRef  string  `json:"service_reference"`
	// Provider selects which scraper backend should process the order.
	// Known values: "delfos", "tiptravel". Defaults to "delfos" when empty.
	Provider string `json:"provider"`
}

// ScrapingJobResponse represents the full job response from API
type ScrapingJobResponse struct {
	ID          string                 `json:"id"`
	AccountID   string                 `json:"account_id"`
	HotelName   string                 `json:"hotel_name"`
	HotelID     string                 `json:"hotel_id"`
	RoomType    string                 `json:"room_type"`
	MealPlan    string                 `json:"meal_plan"`
	BookedPrice float64                `json:"booked_price"`
	Currency    string                 `json:"currency"`
	CheckIn     string                 `json:"check_in"`
	CheckOut    string                 `json:"check_out"`
	Data        map[string]interface{} `json:"data"`
}

// PriceComparisonResponse represents the response from updating price
type PriceComparisonResponse struct {
	JobID              string  `json:"job_id"`
	ServiceRef         string  `json:"service_reference"`
	HotelName          string  `json:"hotel_name"`
	RoomType           string  `json:"room_type"`
	MealPlan           string  `json:"meal_plan"`
	BookedPrice        float64 `json:"booked_price"`
	CurrentPrice       float64 `json:"current_price"`
	PriceDifference    float64 `json:"price_difference"`
	PriceDifferencePct float64 `json:"price_difference_percent"`
	Status             string  `json:"status"`
	Savings            bool    `json:"savings"`
	LastUpdated        string  `json:"last_updated"`
}

// Client for Expeditus API
type Client struct {
	baseURL   string
	accountID string
	client    *http.Client
}

// NewClient creates a new API client
func NewClient() (*Client, error) {
	cfg, err := config.LoadAPIConfig()
	if err != nil {
		return nil, err
	}

	return &Client{
		baseURL:   cfg.BaseURL,
		accountID: cfg.AccountID,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// GetPendingOrders fetches pending scraping orders from the API for the configured account
func (c *Client) GetPendingOrders() ([]ScrapingOrder, error) {
	// If no account configured, get all
	if c.accountID == "" {
		return c.GetAllPendingOrders()
	}

	url := fmt.Sprintf("%s/api/scraper/orders?account_id=%s", c.baseURL, c.accountID)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get orders: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var orders []ScrapingOrder
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, fmt.Errorf("failed to decode orders: %w", err)
	}

	return orders, nil
}

// GetPendingOrdersByAccount fetches pending orders for a specific account
func (c *Client) GetPendingOrdersByAccount(accountID string) ([]ScrapingOrder, error) {
	url := fmt.Sprintf("%s/api/scraper/orders?account_id=%s", c.baseURL, accountID)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get orders: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var orders []ScrapingOrder
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, fmt.Errorf("failed to decode orders: %w", err)
	}

	return orders, nil
}

// GetAllPendingOrders fetches all pending scraping orders from all accounts (Delfos only)
func (c *Client) GetAllPendingOrders() ([]ScrapingOrder, error) {
	// Use empty account_id to get all orders from all Delfos accounts
	url := fmt.Sprintf("%s/api/scraper/orders", c.baseURL)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get all orders: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var orders []ScrapingOrder
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, fmt.Errorf("failed to decode orders: %w", err)
	}

	return orders, nil
}

// UpdatePrice sends the scraped price to the API
func (c *Client) UpdatePrice(jobID string, currentPrice float64, roomFound, mealPlan string) (*PriceComparisonResponse, error) {
	url := fmt.Sprintf("%s/api/scraping_jobs/%s/price", c.baseURL, jobID)

	payload := map[string]interface{}{
		"current_price":   currentPrice,
		"currency":        "US$",
		"room_found":      roomFound,
		"meal_plan_found": mealPlan,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to update price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result PriceComparisonResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetOrderByID fetches a specific order by ID
func (c *Client) GetOrderByID(jobID string) (*ScrapingOrder, error) {
	url := fmt.Sprintf("%s/api/scraping_jobs/%s", c.baseURL, jobID)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Use generic map to parse the response
	var rawResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawResp); err != nil {
		return nil, fmt.Errorf("failed to decode order: %w", err)
	}

	// Extract data field
	data, _ := rawResp["data"].(map[string]interface{})

	// Extract search URL from data.destination_url
	searchURL := ""
	if destURL, ok := data["destination_url"].(string); ok {
		searchURL = destURL
	}

	// Get hotel_id
	hotelID := ""
	if hid, ok := rawResp["hotel_id"].(string); ok {
		hotelID = hid
	} else if hid, ok := data["hotel_id"].(string); ok {
		hotelID = hid
	}

	// Extract passengers from data
	passengers := ""
	if p, ok := data["passengers"].(string); ok {
		passengers = p
	}

	// Build order
	order := &ScrapingOrder{
		ID:          getString(rawResp, "id"),
		SearchURL:   searchURL,
		HotelName:   getString(rawResp, "hotel_name"),
		HotelID:     hotelID,
		RoomType:    getString(rawResp, "room_type"),
		MealPlan:    getString(rawResp, "meal_plan"),
		BookedPrice: getFloat(rawResp, "booked_price"),
		Currency:    getString(rawResp, "currency"),
		CheckIn:     getString(rawResp, "check_in"),
		CheckOut:    getString(rawResp, "check_out"),
		Passengers:  passengers,
		ServiceRef:  getString(rawResp, "id"),
		Provider:    getString(rawResp, "provider"),
	}

	return order, nil
}

// Helper functions for type-safe extraction
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getFloat(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}
