package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"rabbithole/db"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	embedAPI              = "https://api.rabbithole.cred.club/v1/embeddings"
	chatAPI               = "https://api.rabbithole.cred.club/v1/chat/completions"
	apiKey                = "sk-G_BXXmoaRnY5pkImc2yjDw"
	linearAPIKey          = "lin_api_4nhyJjjFfVRW483gOHmQfjFPGHwkGMVFhfXNjVv3" // Replace with your Linear API key
	chatModel             = "claude-3-7-sonnet"
	embedModel            = "text-embedding-3-small"
	maxConcurrentLLMCalls = 5
	llmTimeout            = 15 * time.Second
	maxFeedbacksPerBatch  = 10
	maxEmbeddingBatchSize = 100
)

// Models
type LinearTicket struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"createdAt"`
}

type TicketIntelligence struct {
	TicketID          string   `json:"ticketId"`
	LinkedFeedbacks   []string `json:"linkedFeedbacks"`
	UserImpact        int      `json:"userImpact"`
	MentionCount      int      `json:"mentionCount"`
	WeightedSeverity  float64  `json:"weightedSeverity"`
	SuggestedPriority int      `json:"suggestedPriority"`
	EscalationLevel   string   `json:"escalationLevel"`
}

type FeedbackEmbedding struct {
	ID         int64           `gorm:"primaryKey;autoIncrement"`
	FeedbackID int64           `gorm:"uniqueIndex;not null"`
	Feedback   string          `gorm:"type:text;not null"`
	Embedding  pgvector.Vector `gorm:"type:vector(1536);not null"`
	CreatedAt  time.Time       `gorm:"not null"`
}

type FeedbackInsight struct {
	ID          int64 `gorm:"primaryKey"`
	OriginalID  int64 `gorm:"column:original_id"`
	Source      string
	Username    string
	Feedback    string
	LOB         string `gorm:"column:lob"`
	Category    string
	Subcategory string
	Question    string
	InsightType string `gorm:"column:insight_type"`
	CreatedAt   time.Time
}

// Services
type LinearClient struct {
	httpClient *http.Client
	apiKey     string
	endpoint   string
}

type FeedbackService struct {
	db           *gorm.DB
	rateLimiter  chan struct{}
	linearClient *LinearClient
}

type LinearResponse struct {
	Data struct {
		Issues struct {
			Nodes []struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Priority    int    `json:"priority"`
				CreatedAt   string `json:"createdAt"`
				State       struct {
					Name string `json:"name"`
				} `json:"state"`
				Labels struct {
					Nodes []struct {
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"labels"`
				Team struct {
					Name string `json:"name"`
				} `json:"team"`
			} `json:"nodes"`
		} `json:"issues"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

func NewLinearClient(apiKey string) *LinearClient {
	return &LinearClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiKey:     apiKey,
		endpoint:   "https://api.linear.app/graphql",
	}
}

func NewFeedbackService(db *gorm.DB) (*FeedbackService, error) {
	// Disable SQL logging
	db = db.Session(&gorm.Session{
		Logger: db.Logger.LogMode(logger.Silent),
	})

	// Enable pgvector extension silently
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return nil, fmt.Errorf("failed to create vector extension: %w", err)
	}

	// Create indexes silently
	if err := db.Exec("CREATE INDEX IF NOT EXISTS feedback_embeddings_vector_idx ON feedback_embeddings USING ivfflat (embedding vector_cosine_ops)").Error; err != nil {
		return nil, fmt.Errorf("failed to create vector index: %w", err)
	}

	linearClient := NewLinearClient(linearAPIKey)

	svc := &FeedbackService{
		db:           db,
		rateLimiter:  make(chan struct{}, maxConcurrentLLMCalls),
		linearClient: linearClient,
	}

	return svc, nil
}

func (s *FeedbackService) getEmbeddings(texts []string) ([][]float32, error) {
	s.rateLimiter <- struct{}{}
	defer func() { <-s.rateLimiter }()

	data := map[string]interface{}{
		"input": texts,
		"model": embedModel,
	}

	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", embedAPI, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	embeddings := make([][]float32, len(result.Data))
	for i, data := range result.Data {
		embeddings[i] = data.Embedding
	}

	return embeddings, nil
}

func (lc *LinearClient) executeGraphQL(query string, variables map[string]interface{}) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GraphQL payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", lc.endpoint, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", lc.apiKey)

	resp, err := lc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GraphQL request failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	return responseBody, nil
}

func (lc *LinearClient) GetIssues() ([]LinearTicket, error) {
	query := `
        query {
            issues(first: 100, orderBy: updatedAt) {
                nodes {
                    id
                    title
                    description
                    priority
                    createdAt
                    state {
                        name
                    }
                    labels {
                        nodes {
                            name
                        }
                    }
                    team {
                        name
                    }
                }
            }
        }
    `

	response, err := lc.executeGraphQL(query, nil)
	if err != nil {
		return nil, err
	}

	var linearResp LinearResponse
	if err := json.Unmarshal(response, &linearResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(linearResp.Errors) > 0 {
		return nil, fmt.Errorf("GraphQL error: %s", linearResp.Errors[0].Message)
	}

	var tickets []LinearTicket
	for _, issue := range linearResp.Data.Issues.Nodes {
		createdAt, err := time.Parse(time.RFC3339, issue.CreatedAt)
		if err != nil {
			log.Printf("Warning: failed to parse createdAt for issue %s: %v", issue.ID, err)
			createdAt = time.Now()
		}

		tickets = append(tickets, LinearTicket{
			ID:          issue.ID,
			Title:       issue.Title,
			Description: issue.Description,
			Priority:    issue.Priority,
			CreatedAt:   createdAt,
		})
	}

	return tickets, nil
}

func (lc *LinearClient) UpdateIssuePriority(issueID string, priority int, description string) error {
	mutation := `
        mutation UpdateIssuePriority($issueId: String!, $priority: Int!, $description: String!) {
            issueUpdate(
                id: $issueId,
                input: {
                    priority: $priority,
                    description: $description
                }
            ) {
                success
                issue {
                    id
                    priority
                }
            }
        }
    `

	variables := map[string]interface{}{
		"issueId":     issueID,
		"priority":    priority,
		"description": description,
	}

	response, err := lc.executeGraphQL(mutation, variables)
	if err != nil {
		return err
	}

	var result struct {
		Data struct {
			IssueUpdate struct {
				Success bool `json:"success"`
			} `json:"issueUpdate"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors,omitempty"`
	}

	if err := json.Unmarshal(response, &result); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(result.Errors) > 0 {
		return fmt.Errorf("GraphQL error: %s", result.Errors[0].Message)
	}

	return nil
}

func (s *FeedbackService) updateFeedbackEmbeddings() error {
	var feedbacks []struct {
		ID       int64
		Feedback string
	}

	// Updated query to handle int64 IDs properly
	query := `
        SELECT f.id::bigint, f.feedback 
        FROM feedback_insights f 
        LEFT JOIN feedback_embeddings fe ON f.id = fe.feedback_id
        WHERE fe.id IS NULL
        LIMIT ?
    `

	if err := s.db.Raw(query, maxEmbeddingBatchSize).Scan(&feedbacks).Error; err != nil {
		return fmt.Errorf("failed to query feedbacks: %w", err)
	}

	if len(feedbacks) == 0 {
		return nil
	}

	// Extract texts for embedding
	texts := make([]string, len(feedbacks))
	for i, f := range feedbacks {
		texts[i] = f.Feedback
	}

	// Get embeddings
	embeddings, err := s.getEmbeddings(texts)
	if err != nil {
		return fmt.Errorf("failed to get embeddings: %w", err)
	}

	// Store embeddings in batches
	for i, feedback := range feedbacks {
		vector := pgvector.NewVector(embeddings[i])
		if err != nil {
			log.Printf("Error creating vector for feedback %d: %v", feedback.ID, err)
			continue
		}

		embedding := FeedbackEmbedding{
			FeedbackID: feedback.ID,
			Feedback:   feedback.Feedback,
			Embedding:  vector,
			CreatedAt:  time.Now(),
		}

		if err := s.db.Create(&embedding).Error; err != nil {
			log.Printf("Error storing embedding for feedback %d: %v", feedback.ID, err)
		}
	}

	return nil
}

func (s *FeedbackService) findSimilarFeedbacks(ticketText string) ([]string, error) {
	embeddings, err := s.getEmbeddings([]string{ticketText})
	if err != nil {
		return nil, fmt.Errorf("failed to get embeddings: %w", err)
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	vector := pgvector.NewVector(embeddings[0])

	query := `
        WITH similar_feedbacks AS (
            SELECT 
                fi.feedback,
                fi.lob,
                fi.category,
                1 - (fe.embedding <=> $1::vector) as similarity
            FROM feedback_embeddings fe
            JOIN feedback_insights fi ON fe.feedback_id = fi.id
            WHERE 1 - (fe.embedding <=> $1::vector) > 0.3
            ORDER BY similarity DESC
            LIMIT 15
        )
        SELECT feedback
        FROM similar_feedbacks
    `

	var feedbacks []string
	if err := s.db.Raw(query, vector).Scan(&feedbacks).Error; err != nil {
		log.Printf("Error in similarity search: %v", err)
		return nil, fmt.Errorf("failed to query similar feedbacks: %w", err)
	}

	log.Printf("Found %d similar feedbacks", len(feedbacks))
	return feedbacks, nil
}

func (s *FeedbackService) calculateTicketMetrics(feedbacks []string) (impact int, mentions int, severity float64) {
	if len(feedbacks) == 0 {
		return 0, 0, 1.0
	}

	query := `
        WITH feedback_metrics AS (
            SELECT 
                COUNT(DISTINCT username) as affected_users,
                COUNT(*) as mention_count,
                lob,
                category
            FROM feedback_insights
            WHERE feedback = ANY($1)
            GROUP BY lob, category
        )
        SELECT 
            MAX(affected_users) as affected_users,
            SUM(mention_count) as mention_count
        FROM feedback_metrics
    `

	var result struct {
		AffectedUsers int `gorm:"column:affected_users"`
		MentionCount  int `gorm:"column:mention_count"`
	}

	if err := s.db.Raw(query, pq.Array(feedbacks)).Scan(&result).Error; err != nil {
		log.Printf("Error calculating metrics: %v", err)
		return 0, len(feedbacks), 1.0
	}

	// Calculate severity based on impact and mentions
	calculatedSeverity := float64(result.AffectedUsers*2+result.MentionCount) / 10.0
	if calculatedSeverity < 1.0 {
		calculatedSeverity = 1.0
	}

	log.Printf("Calculated metrics - Impact: %d, Mentions: %d, Severity: %.2f",
		result.AffectedUsers, result.MentionCount, calculatedSeverity)

	return result.AffectedUsers, result.MentionCount, calculatedSeverity
}

func (s *FeedbackService) calculatePriority(impact int, mentions int, severity float64) (priority int, escalation string) {
	// Calculate weighted score
	score := float64(impact*3+mentions) * severity // Increased impact weight

	// More granular priority levels
	switch {
	case score > 150:
		return 0, "URGENT" // Added urgent level
	case score > 100:
		return 1, "CRITICAL"
	case score > 40: // Lowered threshold
		return 2, "HIGH"
	case score > 15: // Lowered threshold
		return 3, "MEDIUM"
	default:
		return 4, "LOW"
	}
}

func (s *FeedbackService) ProcessTicketIntelligence() ([]TicketIntelligence, error) {
	log.Println("Starting ticket intelligence processing")

	tickets, err := s.linearClient.GetIssues()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Linear tickets: %w", err)
	}
	log.Printf("Processing %d Linear tickets", len(tickets))

	var intelligence []TicketIntelligence
	resultChan := make(chan TicketIntelligence, len(tickets))
	errorChan := make(chan error, len(tickets))

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5)

	for _, ticket := range tickets {
		wg.Add(1)
		go func(t LinearTicket) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			ticketText := strings.TrimSpace(t.Title + " " + t.Description)
			feedbacks, err := s.findSimilarFeedbacks(ticketText)
			if err != nil {
				errorChan <- fmt.Errorf("error processing ticket %s: %w", t.ID, err)
				return
			}

			impact, mentions, severity := s.calculateTicketMetrics(feedbacks)
			priority, escalation := s.calculatePriority(impact, mentions, severity)

			ti := TicketIntelligence{
				TicketID:          t.ID,
				LinkedFeedbacks:   feedbacks,
				UserImpact:        impact,
				MentionCount:      mentions,
				WeightedSeverity:  severity,
				SuggestedPriority: priority,
				EscalationLevel:   escalation,
			}

			if len(feedbacks) > 0 {
				log.Printf("Ticket %s: Found %d feedbacks, Priority: %d, Escalation: %s",
					t.ID, len(feedbacks), priority, escalation)

				if priority != t.Priority {
					if err := s.updateLinearTicketPriority(t.ID, priority, escalation); err != nil {
						log.Printf("Failed to update Linear ticket priority: %v", err)
					}
					if err := s.updateTicketWithInsights(t.ID, ti); err != nil {
						log.Printf("Failed to add feedback insights comment: %v", err)
					}
				}
			}

			resultChan <- ti
		}(ticket)
	}

	// Wait for all goroutines to finish
	go func() {
		wg.Wait()
		close(resultChan)
		close(errorChan)
	}()

	// Collect results
	for ti := range resultChan {
		intelligence = append(intelligence, ti)
	}

	// Sort by severity
	sort.Slice(intelligence, func(i, j int) bool {
		return intelligence[i].WeightedSeverity > intelligence[j].WeightedSeverity
	})

	return intelligence, nil
}

func (s *FeedbackService) updateLinearTicketPriority(ticketID string, priority int, escalation string) error {
	description := fmt.Sprintf("🚨 Auto-escalated to %s based on user feedback analysis", escalation)
	return s.linearClient.UpdateIssuePriority(ticketID, priority, description)
}

func (s *FeedbackService) updateTicketWithInsights(ticketID string, intelligence TicketIntelligence) error {
	comment := fmt.Sprintf(`
📊 Feedback Analysis Update:

Impact Metrics:
- User Impact: %d affected users
- Mention Count: %d mentions
- Weighted Severity: %.2f
- Suggested Priority: %d
- Escalation Level: %s

🔍 Related Feedback:
%s

This analysis is based on user feedback patterns and impact metrics.
    `,
		intelligence.UserImpact,
		intelligence.MentionCount,
		intelligence.WeightedSeverity,
		intelligence.SuggestedPriority,
		intelligence.EscalationLevel,
		strings.Join(intelligence.LinkedFeedbacks, "\n- "),
	)

	return s.linearClient.AddIssueComment(ticketID, comment)
}

func (lc *LinearClient) AddIssueComment(issueID string, comment string) error {
	mutation := `
        mutation CreateComment($issueId: String!, $body: String!) {
            commentCreate(
                input: {
                    issueId: $issueId,
                    body: $body
                }
            ) {
                success
                comment {
                    id
                }
            }
        }
    `

	variables := map[string]interface{}{
		"issueId": issueID,
		"body":    comment,
	}

	response, err := lc.executeGraphQL(mutation, variables)
	if err != nil {
		return err
	}

	var result struct {
		Data struct {
			CommentCreate struct {
				Success bool `json:"success"`
			} `json:"commentCreate"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors,omitempty"`
	}

	if err := json.Unmarshal(response, &result); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(result.Errors) > 0 {
		return fmt.Errorf("GraphQL error: %s", result.Errors[0].Message)
	}

	return nil
}

func (s *FeedbackService) ensureIndexes() error {
	// Add indexes for feedback_insights table
	queries := []string{
		`CREATE INDEX IF NOT EXISTS idx_feedback_insights_feedback ON feedback_insights(feedback)`,
		`CREATE INDEX IF NOT EXISTS idx_feedback_insights_username ON feedback_insights(username)`,
		`CREATE INDEX IF NOT EXISTS idx_feedback_insights_category ON feedback_insights(category)`,
	}

	for _, query := range queries {
		if err := s.db.Exec(query).Error; err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	return nil
}

func (s *FeedbackService) verifyFeedbackEmbeddings() error {
	// Check for feedback insights without embeddings
	var missingCount int64
	query := `
        SELECT COUNT(*)
        FROM feedback_insights fi
        LEFT JOIN feedback_embeddings fe ON fi.id = fe.feedback_id
        WHERE fe.id IS NULL
    `
	if err := s.db.Raw(query).Scan(&missingCount).Error; err != nil {
		return fmt.Errorf("failed to check missing embeddings: %w", err)
	}

	if missingCount > 0 {
		log.Printf("Found %d feedback insights without embeddings, generating...", missingCount)
		if err := s.updateFeedbackEmbeddings(); err != nil {
			return fmt.Errorf("failed to update embeddings: %w", err)
		}
	}

	return nil
}

func main() {
	// Initialize your database connection here
	database, err := db.InitDB()
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}

	// Get the underlying *sql.DB to close it properly
	sqlDB, err := database.DB()
	if err != nil {
		log.Fatal("Failed to get underlying *sql.DB:", err)
	}
	defer sqlDB.Close()

	// Initialize feedback service
	feedbackService, err := NewFeedbackService(database)
	if err != nil {
		log.Fatal("Failed to create feedback service:", err)
	}

	// Initialize Gin router
	r := gin.Default()

	// Add the ticket intelligence endpoint
	r.GET("/api/v1/ticket-intelligence", func(c *gin.Context) {
		intelligence, err := feedbackService.ProcessTicketIntelligence()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, intelligence)
	})

	// Add the debug endpoint
	r.GET("/api/v1/debug/embeddings", func(c *gin.Context) {
		var stats struct {
			TotalFeedbacks int64   `json:"totalFeedbacks"`
			WithEmbeddings int64   `json:"withEmbeddings"`
			Coverage       float64 `json:"coverage"`
		}

		// Get total feedbacks
		feedbackService.db.Model(&FeedbackInsight{}).Count(&stats.TotalFeedbacks)
		feedbackService.db.Model(&FeedbackEmbedding{}).Count(&stats.WithEmbeddings)

		if stats.TotalFeedbacks > 0 {
			stats.Coverage = float64(stats.WithEmbeddings) / float64(stats.TotalFeedbacks) * 100
		}

		c.JSON(http.StatusOK, stats)
	})

	// Create server with proper shutdown
	srv := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}

	// Graceful shutdown
	go func() {
		log.Printf("🚀 Server starting on http://localhost:8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server failed to start: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit

	// Shutdown gracefully
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("Server exiting")
}
