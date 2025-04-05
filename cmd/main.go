package main

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"rabbithole/db"
	"rabbithole/feed"
	"rabbithole/insights"
	"rabbithole/models"
	"rabbithole/predictions"
	"rabbithole/scripts/fetchdata"
	"rabbithole/scripts/linear"
	"rabbithole/scripts/roadmap_generator"
	"rabbithole/scripts/ticket_intelligence"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// FeedbackService holds all the service dependencies
type FeedbackService struct {
	db           *gorm.DB
	ticketSvc    *ticket_intelligence.FeedbackService
	fetchSvc     *fetchdata.FeedbackService
	insightsSvc  *insights.InsightsService
	linearSvc    *linear.LinearService
	roadmapSvc   *roadmap_generator.RoadmapService
	predictorSvc *predictions.Predictor
	feedSvc      *feed.FeedService
}

// NewFeedbackService initializes all required services
func NewFeedbackService(db *gorm.DB) (*FeedbackService, error) {
	ticketSvc, err := ticket_intelligence.NewFeedbackService(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create ticket intelligence service: %w", err)
	}

	fetchSvc, err := fetchdata.NewFeedbackService(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create fetch data service: %w", err)
	}

	insightsSvc, err := insights.NewInsightsService(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create insights service: %w", err)
	}

	linearSvc, err := linear.NewLinearService(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create linear service: %w", err)
	}

	roadmapSvc, err := roadmap_generator.NewRoadmapService(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create roadmap service: %w", err)
	}

	predictorSvc, err := predictions.NewPredictor(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create predictor service: %w", err)
	}

	feedSvc, err := feed.NewFeedService(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create feed service: %w", err)
	}

	return &FeedbackService{
		db:           db,
		ticketSvc:    ticketSvc,
		fetchSvc:     fetchSvc,
		insightsSvc:  insightsSvc,
		linearSvc:    linearSvc,
		roadmapSvc:   roadmapSvc,
		predictorSvc: predictorSvc,
		feedSvc:      feedSvc,
	}, nil
}

func main() {
	log.Println("🚀 Starting feedback service")

	// Initialize database
	database, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ Database initialization failed:", err)
	}

	// Get the underlying *sql.DB to close it properly
	sqlDB, err := database.DB()
	if err != nil {
		log.Fatal("❌ Failed to get underlying *sql.DB:", err)
	}
	defer sqlDB.Close()

	// Initialize services
	feedbackService, err := NewFeedbackService(database)
	if err != nil {
		log.Fatal("❌ Failed to initialize feedback service:", err)
	}

	// Initialize Gin router
	r := gin.Default()

	// Configure CORS middleware
	config := cors.Config{
		AllowOrigins:     []string{"*"}, // Allow all origins
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}
	r.Use(cors.New(config))

	// Register routes
	v1 := r.Group("/api/v1")
	{
		v1.GET("/feedback", func(c *gin.Context) {
			lob := c.Query("lob")
			if lob == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "lob is required"})
				return
			}

			category := c.Query("category")
			subCategory := c.Query("sub_category")

			resp, err := feedbackService.insightsSvc.GetTopFeedback(c.Request.Context(), lob, category, subCategory)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			c.JSON(http.StatusOK, resp)
		})

		v1.GET("/feedback/insights", func(c *gin.Context) {
			resp, err := feedbackService.insightsSvc.GetTopInsights(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, resp)
		})

		v1.POST("/generate", func(c *gin.Context) {
			resp, err := feedbackService.fetchSvc.GenerateData(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, resp)
		})

		v1.GET("/ticket-intelligence", func(c *gin.Context) {
			resp, err := feedbackService.ticketSvc.ProcessTickets(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, resp)
		})

		v1.POST("/generate-tickets", func(c *gin.Context) {
			resp, err := feedbackService.linearSvc.GenerateTickets(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, resp)
		})

		v1.GET("/roadmap", func(c *gin.Context) {
			lob := c.Query("lob")
			if lob == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "lob is required"})
				return
			}

			category := c.Query("category")

			resp, err := feedbackService.roadmapSvc.GenerateRoadmap(c.Request.Context(), lob, category)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, resp)
		})

		// Add new prediction endpoints
		v1.POST("/predictions/process-all", func(c *gin.Context) {
			log.Println("Starting feedback processing...")
			err := feedbackService.predictorSvc.ProcessAllFeedback()
			if err != nil {
				log.Printf("Failed to process feedback: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": fmt.Sprintf("Failed to process feedback: %v", err),
				})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"message": "Successfully processed all feedback",
			})
		})

		// Endpoint to get prediction status
		v1.GET("/predictions/status", func(c *gin.Context) {
			var totalCount, processedCount int64

			// Count total feedback
			if err := feedbackService.db.Model(&models.Feedback{}).Count(&totalCount).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get total count"})
				return
			}

			// Count processed feedback
			if err := feedbackService.db.Model(&models.Feedback{}).Where("has_prediction = ?", true).Count(&processedCount).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get processed count"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"total_feedback":      totalCount,
				"processed_feedback":  processedCount,
				"pending_feedback":    totalCount - processedCount,
				"progress_percentage": float64(processedCount) / float64(totalCount) * 100,
			})
		})

		// Endpoint to get predictions for specific feedback
		v1.GET("/predictions/feedback/:id", func(c *gin.Context) {
			var feedback models.Feedback
			if err := feedbackService.db.Preload("Prediction").First(&feedback, c.Param("id")).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Feedback not found"})
				return
			}

			if !feedback.HasPrediction {
				c.JSON(http.StatusOK, gin.H{
					"feedback_id": feedback.ID,
					"status":      "not_processed",
				})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"feedback_id": feedback.ID,
				"prediction":  feedback.Prediction,
				"status":      "processed",
			})
		})

		// Endpoint to process single feedback
		v1.POST("/predictions/feedback/:id", func(c *gin.Context) {
			var feedback models.Feedback
			if err := feedbackService.db.First(&feedback, c.Param("id")).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Feedback not found"})
				return
			}

			if err := feedbackService.predictorSvc.ProcessFeedback(&feedback); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": fmt.Sprintf("Failed to process feedback: %v", err),
				})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"message":     "Successfully processed feedback",
				"feedback_id": feedback.ID,
			})
		})

		// Add new feed endpoint
		v1.GET("/feed", func(c *gin.Context) {
			page := 1
			pageSize := 10

			// Parse pagination parameters
			if p := c.Query("page"); p != "" {
				if val, err := strconv.Atoi(p); err == nil && val > 0 {
					page = val
				}
			}
			if ps := c.Query("page_size"); ps != "" {
				if val, err := strconv.Atoi(ps); err == nil && val > 0 {
					pageSize = val
				}
			}

			feedbacks, total, err := feedbackService.feedSvc.GetAllFeedback(c.Request.Context(), page, pageSize)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"data": feedbacks,
				"meta": gin.H{
					"total":     total,
					"page":      page,
					"page_size": pageSize,
					"pages":     int(math.Ceil(float64(total) / float64(pageSize))),
				},
			})
		})
	}

	// Start server with proper error handling
	srv := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}

	log.Printf("🚀 Server starting on http://localhost:8080")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
