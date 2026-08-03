package handlers

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"ExpeditusClient/internal/api"
	"ExpeditusClient/internal/web/models"
	"ExpeditusClient/internal/web/services"
	"github.com/gin-gonic/gin"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

var scraperService *services.ScraperService
var apiClient *api.Client

func InitScraper() error {
	var err error
	scraperService, err = services.NewScraperService()
	if err != nil {
		return err
	}

	// Initialize API client
	apiClient, err = api.NewClient()
	if err != nil {
		log.Printf("Warning: Failed to initialize API client: %v (scraping will work but won't sync with API)", err)
		apiClient = nil
	}

	return nil
}

type ScrapingHandler struct{}

func NewScrapingHandler() *ScrapingHandler {
	return &ScrapingHandler{}
}

// GetOrders returns pending orders from the API
// Query params:
//   - account_id: filter by account
//   - provider: filter by provider ("delfos" | "tiptravel")
func (h *ScrapingHandler) GetOrders(c *gin.Context) {
	if apiClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "API client not initialized"})
		return
	}

	accountID := c.Query("account_id")
	providerFilter := c.Query("provider")

	var orders []api.ScrapingOrder
	var err error

	if accountID != "" {
		orders, err = apiClient.GetPendingOrdersByAccount(accountID)
	} else {
		orders, err = apiClient.GetAllPendingOrders()
	}

	if err != nil {
		log.Printf("GetOrders error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if providerFilter != "" {
		filtered := make([]api.ScrapingOrder, 0, len(orders))
		for _, o := range orders {
			if o.Provider == providerFilter {
				filtered = append(filtered, o)
			}
		}
		orders = filtered
	}

	c.JSON(http.StatusOK, gin.H{
		"orders": orders,
		"count":  len(orders),
	})
}

// StartScrapingJob starts scraping for a specific job
func (h *ScrapingHandler) StartScrapingJob(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "job_id is required"})
		return
	}

	log.Printf("StartScrapingJob called with jobID: %s", jobID)

	// Get order details from API
	var order *api.ScrapingOrder
	var err error

	if apiClient == nil {
		log.Printf("Warning: API client is nil, using default order")
		order = nil
	} else {
		order, err = apiClient.GetOrderByID(jobID)
		if err != nil {
			log.Printf("GetOrderByID error: %v", err)
			c.JSON(http.StatusNotFound, gin.H{"error": "Order not found in API: " + err.Error()})
			return
		}
		if order == nil {
			log.Printf("GetOrderByID returned nil order for jobID: %s", jobID)
			c.JSON(http.StatusNotFound, gin.H{"error": "Order not found in API for job: " + jobID})
			return
		}
		log.Printf("StartScrapingJob: got order for %s - Hotel: %s, BookedPrice: %.2f", jobID, order.HotelName, order.BookedPrice)
	}

	sessionID := generateSessionID()

	// Start scraping with order data
	err = scraperService.StartSessionWithOrder(sessionID, order)
	if err != nil {
		log.Printf("StartSession error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	log.Printf("Session %s started successfully for job %s (hotel: %s)", sessionID, jobID, getHotelName(order))

	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": sessionID,
		"jobId":     jobID,
		"status":    models.SessionStatusRunning,
		"message":   "Scraping session started",
		"order":     order,
	})
}


// StartBatchScraping starts scraping for multiple orders SEQUENTIALLY (one by one)
func (h *ScrapingHandler) StartBatchScraping(c *gin.Context) {
	var request struct {
		JobIDs []string `json:"job_ids"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	if len(request.JobIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No job_ids provided"})
		return
	}

	log.Printf("StartBatchScraping called with %d jobs (sequential processing)", len(request.JobIDs))

	var sessionIDs []string
	var failedJobs []string

	// Process jobs SEQUENTIALLY (one by one)
	for i, jobID := range request.JobIDs {
		log.Printf("Processing job %d/%d: %s", i+1, len(request.JobIDs), jobID)
		
		// Get order from API
		var order *api.ScrapingOrder
		var err error
		
		if apiClient != nil {
			order, err = apiClient.GetOrderByID(jobID)
			if err != nil {
				log.Printf("GetOrderByID error for %s: %v", jobID, err)
				failedJobs = append(failedJobs, jobID)
				continue
			}
			if order == nil {
				log.Printf("Order not found for jobID: %s", jobID)
				failedJobs = append(failedJobs, jobID)
				continue
			}
			log.Printf("Order %s: HotelName='%s', HotelID='%s'", 
				jobID, order.HotelName, order.HotelID)
		} else {
			order = &api.ScrapingOrder{
				ID:           jobID,
				HotelName:    "Test Hotel",
				SearchURL:    "https://www.delfos.tur.ar/home?directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL&&departureDate=01/01/2030&arrivalDate=07/01/2030&hotelDestination=Destination::UNKNOWN",
			}
		}
		
		sessionID := generateSessionID()
		
		err = scraperService.StartSessionWithOrder(sessionID, order)
		if err != nil {
			log.Printf("StartSession error for %s: %v", jobID, err)
			failedJobs = append(failedJobs, jobID)
			continue
		}
		
		log.Printf("Session %s started for job %s", sessionID, jobID)
		sessionIDs = append(sessionIDs, sessionID)
		
		// Wait for this session to complete before starting the next one
		// Poll for status until it's no longer "running"
		log.Printf("Waiting for session %s to complete...", sessionID)
		for {
			time.Sleep(5 * time.Second)
			
			session, exists := scraperService.GetSession(sessionID)
			if !exists || session.Status == "completed" || session.Status == "cancelled" || session.Status == "error" {
				break
			}
			log.Printf("Session %s still running (status: %s)...", sessionID, session.Status)
		}
		
		session, _ := scraperService.GetSession(sessionID)
		status := ""
		if session != nil {
			status = session.Status
		}
		log.Printf("Session %s completed with status: %s", sessionID, status)
	}

	log.Printf("Sequential scraping completed: %d sessions, %d failed", len(sessionIDs), len(failedJobs))

	c.JSON(http.StatusAccepted, gin.H{
		"sessionIds":  sessionIDs,
		"failedJobs":  failedJobs,
		"totalJobs":   len(request.JobIDs),
		"message":     fmt.Sprintf("Processed %d jobs sequentially", len(sessionIDs)),
	})
}


func getHotelName(order *api.ScrapingOrder) string {
	if order == nil {
		return "nil"
	}
	return fmt.Sprintf("%s (%.2f)", order.HotelName, order.BookedPrice)
}

// StartScraping starts a scraping session (legacy endpoint)
func (h *ScrapingHandler) StartScraping(c *gin.Context) {
	sessionID := c.GetHeader("X-Session-ID")
	if sessionID == "" {
		sessionID = generateSessionID()
	}

	log.Printf("StartScraping called with sessionID: %s", sessionID)

	err := scraperService.StartSession(sessionID)
	if err != nil {
		log.Printf("StartSession error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	log.Printf("Session %s started successfully", sessionID)

	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": sessionID,
		"status":    models.SessionStatusRunning,
		"message":   "Scraping session started",
	})
}

func (h *ScrapingHandler) GetStatus(c *gin.Context) {
	sessionID := c.Param("sessionId")

	status, progress := scraperService.GetStatus(sessionID)
	if status == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"sessionId": sessionID,
		"status":    status,
		"progress":  progress,
	})
}

func (h *ScrapingHandler) GetProgress(c *gin.Context) {
	sessionID := c.Param("sessionId")

	session, exists := scraperService.GetSession(sessionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	progress := &models.ProgressUpdate{
		SessionID:  session.ID,
		Stage:      session.Progress.Stage,
		Progress:   session.Progress.Progress,
		Processed:  session.Progress.Processed,
		TotalItems: session.Progress.TotalItems,
		Speed:      session.Progress.Speed,
		ETA:        session.Progress.ETA,
		Timestamp:  session.Progress.Timestamp,
	}

	c.JSON(http.StatusOK, progress)
}

func (h *ScrapingHandler) GetResults(c *gin.Context) {
	sessionID := c.Param("sessionId")

	// If sessionId contains commas, handle multiple sessions
	if strings.Contains(sessionID, ",") {
		sessionIDs := strings.Split(sessionID, ",")
		var allResults []map[string]interface{}

		for _, sid := range sessionIDs {
			sid = strings.TrimSpace(sid)
			if sid == "" {
				continue
			}

			session, exists := scraperService.GetSession(sid)
			if !exists {
				continue
			}

			for _, r := range session.Results {
				allResults = append(allResults, map[string]interface{}{
					"sessionId": session.ID,
					"result":    r,
				})
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"results": allResults,
			"count":   len(allResults),
		})
		return
	}

	// If sessionId is "latest", return results from all recent sessions
	if sessionID == "latest" {
		// Get all recent sessions with results
		sessions := scraperService.GetAllSessions()

		var allResults []map[string]interface{}
		for _, s := range sessions {
			for _, r := range s.Results {
				allResults = append(allResults, map[string]interface{}{
					"sessionId": s.ID,
					"result":    r,
				})
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"results": allResults,
			"count":   len(allResults),
		})
		return
	}

	session, exists := scraperService.GetSession(sessionID)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"sessionId": session.ID,
		"status":    session.Status,
		"results":   session.Results,
	})
}

func (h *ScrapingHandler) CancelSession(c *gin.Context) {
	sessionID := c.Param("sessionId")

	err := scraperService.CancelSession(sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"sessionId": sessionID,
		"status":    models.SessionStatusCancelled,
	})
}

// Submit2FACode receives the 2FA code typed by the user in the web UI and
// forwards it to the scraping session, which types it into the page and
// continues with the scraping flow.
func (h *ScrapingHandler) Submit2FACode(c *gin.Context) {
	sessionID := c.Param("sessionId")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	var request struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	code := strings.TrimSpace(request.Code)
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	if err := scraperService.Submit2FA(sessionID, code); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("2FA code submitted for session %s", sessionID)
	c.JSON(http.StatusOK, gin.H{
		"sessionId": sessionID,
		"message":   "2FA code accepted, bot is continuing",
	})
}

// Needs2FA returns whether the session is currently waiting for a 2FA code.
// The web UI polls this endpoint to know when to display the input field.
func (h *ScrapingHandler) Needs2FA(c *gin.Context) {
	sessionID := c.Param("sessionId")
	waiting, msg := scraperService.Needs2FA(sessionID)
	c.JSON(http.StatusOK, gin.H{
		"sessionId":  sessionID,
		"needs2FA":   waiting,
		"message":    msg,
	})
}

func generateSessionID() string {
	return "session-" + randomString(12)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// GetAllSessionsDebug returns all sessions for debugging
func GetAllSessionsDebug() []map[string]interface{} {
	if scraperService == nil {
		return []map[string]interface{}{}
	}

	sessions := scraperService.GetAllSessions()
	var result []map[string]interface{}

	for _, s := range sessions {
		hotelName := ""
		bookedPrice := 0.0
		orderID := ""

		if s.Order != nil {
			hotelName = s.Order.HotelName
			bookedPrice = s.Order.BookedPrice
			orderID = s.Order.ID
		}

		// Extract result data if available
		var resultsData []map[string]interface{}
		for _, r := range s.Results {
			resultsData = append(resultsData, map[string]interface{}{
				"id":            r.ID,
				"data":          r.Data,
				"timestamp":     r.Timestamp,
				"hasScreenshot": r.Screenshot != "",
			})
		}

		result = append(result, map[string]interface{}{
			"sessionId":    s.ID,
			"orderId":      orderID,
			"hotelName":    hotelName,
			"bookedPrice":  bookedPrice,
			"status":       s.Status,
			"resultsCount": len(s.Results),
			"results":      resultsData,
			"progress":     s.Progress.Progress,
			"stage":        s.Progress.Stage,
			"startTime":    s.StartTime,
			"endTime":      s.EndTime,
		})
	}

	return result
}
