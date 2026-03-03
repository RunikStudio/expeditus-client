package handlers

import (
	"log"
	"math/rand"
	"net/http"
	"time"

	"ExpeditusClient/internal/web/models"
	"ExpeditusClient/internal/web/services"
	"github.com/gin-gonic/gin"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

var scraperService *services.ScraperService

func InitScraper() error {
	var err error
	scraperService, err = services.NewScraperService()
	return err
}

type ScrapingHandler struct{}

func NewScrapingHandler() *ScrapingHandler {
	return &ScrapingHandler{}
}

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
