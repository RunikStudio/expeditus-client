package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ExpeditusClient/internal/browser"
	delfosprov "ExpeditusClient/internal/scrapers/delfos"
	"ExpeditusClient/internal/scrapers/tiptravel"
	"ExpeditusClient/internal/web/handlers"
	"ExpeditusClient/internal/web/ws"
	"github.com/gin-gonic/gin"
)

type Config struct {
	Port         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

func DefaultConfig() Config {
	return Config{
		Port:         getEnv("PORT", "8081"),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func main() {
	log.Println("Checking for Chromium...")

	if !browser.ChromeAvailable() {
		log.Println("Chromium not found. Attempting to install automatically...")
		if err := browser.EnsureChrome(); err != nil {
			log.Printf("Warning: Could not install Chromium automatically: %v", err)
			log.Println("The application will try to use system Chromium or download on first use.")
		}
	}

	config := DefaultConfig()

	gin.SetMode(gin.ReleaseMode)

	// Register scraper providers BEFORE handlers.InitScraper() so the
	// scrapers registry is populated when ScraperService dispatches.
	// The delfos provider always registers; tiptravel registers even if
	// the password is missing - it will return an error from the factory
	// when an order actually targets tiptravel without a password set.
	delfosprov.Register()
	if err := tiptravel.Register(); err != nil {
		log.Printf("Warning: tiptravel provider not registered: %v", err)
	} else {
		log.Println("Registered scraper providers: delfos, tiptravel")
	}

	if err := handlers.InitScraper(); err != nil {
		log.Fatalf("Failed to initialize scraper: %v", err)
	}

	ws.Init()

	scrapingHandler := handlers.NewScrapingHandler()

	router := setupRouter(config, scrapingHandler)

	srv := &http.Server{
		Addr:         ":" + config.Port,
		Handler:      router,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		IdleTimeout:  config.IdleTimeout,
	}

	go func() {
		log.Printf("Server starting on port %s", config.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited properly")
}

func setupRouter(config Config, scraper *handlers.ScrapingHandler) *gin.Engine {
	router := gin.New()

	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"time":   time.Now().Format(time.RFC3339),
		})
	})

	api := router.Group("/api")
	{
		// Legacy endpoints
		api.POST("/scrap/start", scraper.StartScraping)
		api.GET("/scrap/status/:sessionId", scraper.GetStatus)
		api.GET("/scrap/progress/:sessionId", scraper.GetProgress)
		api.GET("/scrap/results/:sessionId", scraper.GetResults)
		api.DELETE("/scrap/session/:sessionId", scraper.CancelSession)

		// New endpoints for API integration
		api.GET("/client/orders", scraper.GetOrders)                // Get pending orders from API
		api.POST("/client/scrape/:jobId", scraper.StartScrapingJob)         // Start scraping a specific job
	api.POST("/client/scrape/batch", scraper.StartBatchScraping) // Start multiple scrapes in parallel (max 4)
		api.POST("/client/scrape/session/:sessionId/2fa", scraper.Submit2FACode) // Submit 2FA code provided by the user
		api.GET("/client/scrape/session/:sessionId/2fa", scraper.Needs2FA)        // Poll whether the session is waiting for a 2FA code
	}

	router.GET("/ws", ws.HandleWebSocket)

	router.Static("/static", "./internal/web/static")

	router.LoadHTMLGlob("internal/web/templates/*.html")

	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	router.GET("/progress", func(c *gin.Context) {
		c.HTML(http.StatusOK, "progress.html", nil)
	})

	router.GET("/results", func(c *gin.Context) {
		c.HTML(http.StatusOK, "results.html", nil)
	})

	// Debug endpoint to see all active sessions
	router.GET("/debug/sessions", func(c *gin.Context) {
		sessions := handlers.GetAllSessionsDebug()
		c.JSON(http.StatusOK, gin.H{
			"sessions": sessions,
			"count":    len(sessions),
		})
	})

	return router
}
