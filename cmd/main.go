package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"rabbithole/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	chatAPI               = "https://api.rabbithole.cred.club/v1/chat/completions"
	apiKey                = "sk-G_BXXmoaRnY5pkImc2yjDw"
	chatModel             = "claude-3-7-sonnet"
	maxConcurrentLLMCalls = 5
	llmTimeout            = 15 * time.Second
	maxFeedbacksPerBatch  = 10
)

// Twitter handles to be used in future social media integration
var twitterHandles = []string{"CRED_club", "Cred_support"}

type FeedbackResponse struct {
	LOB          string         `json:"lob"`
	Category     string         `json:"category,omitempty"`
	SubCategory  string         `json:"sub_category,omitempty"`
	FeatureReqs  []FeedbackItem `json:"feature_requests"`
	Improvements []FeedbackItem `json:"improvements"`
	Complaints   []FeedbackItem `json:"complaints"`
}

type FeedbackItem struct {
	Feedback    string  `json:"feedback"`
	UserCount   int     `json:"user_count"`
	Score       float64 `json:"score"`
	AffinityAvg float64 `json:"affinity_avg"`
}

type InsightSummary struct {
	RequestedFeatures  []string `json:"requested_features"` // List of specific features
	ImprovementSummary string   `json:"improvement_summary"`
	ComplaintSummary   string   `json:"complaint_summary"`
}

type CategoryCount struct {
	Name      string         `json:"name"`
	Count     int            `json:"count"`
	UserCount int            `json:"user_count"`
	AvgWeight float64        `json:"avg_weight"`
	Summary   InsightSummary `json:"summary"` // Add this field
}

type InsightsResponse struct {
	FeatureRequests struct {
		TopLOBs          []CategoryCount `json:"top_lobs"`
		TopCategories    []CategoryCount `json:"top_categories"`
		TopSubcategories []CategoryCount `json:"top_subcategories"`
	} `json:"feature_requests"`
	Improvements struct {
		TopLOBs          []CategoryCount `json:"top_lobs"`
		TopCategories    []CategoryCount `json:"top_categories"`
		TopSubcategories []CategoryCount `json:"top_subcategories"`
	} `json:"improvements"`
	Complaints struct {
		TopLOBs          []CategoryCount `json:"top_lobs"`
		TopCategories    []CategoryCount `json:"top_categories"`
		TopSubcategories []CategoryCount `json:"top_subcategories"`
	} `json:"complaints"`
}

type FeedbackService struct {
	db          *gorm.DB
	rateLimiter chan struct{} // For API rate limiting
}

func NewFeedbackService(db *gorm.DB) *FeedbackService {
	return &FeedbackService{
		db:          db,
		rateLimiter: make(chan struct{}, 5), // Allow 5 concurrent API calls
	}
}

func (s *FeedbackService) GetTopFeedback(lob, category, subCategory string) (*FeedbackResponse, error) {
	if lob == "" {
		return nil, fmt.Errorf("lob is required")
	}

	// SQL query that matches our working PostgreSQL query
	query := `
		WITH feedback_scores AS (
			SELECT 
				f.feedback,
				f.insight_type,
				COUNT(DISTINCT f.username) as user_count,
				AVG(COALESCE(ua.weight, 1.0)) as affinity_avg,
				COUNT(DISTINCT f.username) * AVG(COALESCE(ua.weight, 1.0)) as weighted_score
			FROM feedback_insights f
			LEFT JOIN user_affinities ua ON 
				f.category = ua.category
			WHERE f.lob = $1
			GROUP BY f.feedback, f.insight_type
		)
		SELECT * FROM feedback_scores
		ORDER BY weighted_score DESC
	`

	rows, err := s.db.Raw(query, lob).Rows()
	if err != nil {
		return nil, fmt.Errorf("failed to query feedback: %w", err)
	}
	defer rows.Close()

	var (
		featureReqs  []FeedbackItem
		improvements []FeedbackItem
		complaints   []FeedbackItem
	)

	for rows.Next() {
		var item struct {
			Feedback      string
			InsightType   string
			UserCount     int
			AffinityAvg   float64
			WeightedScore float64
		}
		if err := rows.Scan(&item.Feedback, &item.InsightType, &item.UserCount, &item.AffinityAvg, &item.WeightedScore); err != nil {
			return nil, fmt.Errorf("failed to scan feedback: %w", err)
		}

		feedbackItem := FeedbackItem{
			Feedback:    item.Feedback,
			UserCount:   item.UserCount,
			Score:       item.WeightedScore,
			AffinityAvg: item.AffinityAvg,
		}

		switch item.InsightType {
		case "feature-request":
			featureReqs = append(featureReqs, feedbackItem)
		case "improvement":
			improvements = append(improvements, feedbackItem)
		case "complaint":
			complaints = append(complaints, feedbackItem)
		}
	}

	// Check for errors after rows iteration
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over rows: %w", err)
	}

	return &FeedbackResponse{
		LOB:          lob,
		Category:     category,
		SubCategory:  subCategory,
		FeatureReqs:  getTop5(featureReqs),
		Improvements: getTop5(improvements),
		Complaints:   getTop5(complaints),
	}, nil
}

func (s *FeedbackService) categorizeFeedbackWithLLM(feedback string) (string, error) {
	// Acquire rate limit token
	s.rateLimiter <- struct{}{}
	defer func() { <-s.rateLimiter }()

	prompt := fmt.Sprintf(`You are a classification assistant.

Your task is to classify the given user feedback into one of the following insight types:
- complaint: If the user is reporting a problem or expressing frustration.
- improvement: If the user suggests enhancing something that exists.
- feature-request: If the user is asking for a new feature or functionality.

Context:
Feedback: "%s"

Respond with exactly one of the 3 options: complaint, improvement, or feature-request. Do not include anything else.`, feedback)

	data := map[string]interface{}{
		"model": chatModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", chatAPI, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Handle non-200 status codes
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no classification result")
	}

	// Validate the response is one of the expected values
	response := strings.TrimSpace(strings.ToLower(result.Choices[0].Message.Content))
	validResponses := map[string]bool{
		"complaint":       true,
		"improvement":     true,
		"feature-request": true,
	}

	if !validResponses[response] {
		return "", fmt.Errorf("invalid classification: %s", response)
	}

	return response, nil
}

func getTop5(items []FeedbackItem) []FeedbackItem {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})

	if len(items) > 5 {
		return items[:5]
	}
	return items
}

func (s *FeedbackService) summarizeWithLLM(feedbacks []string, insightType string) (string, error) {
	var prompt string
	switch insightType {
	case "feature requests":
		prompt = fmt.Sprintf(`You are a product insights analyst.
Task: Analyze these user feedback items and extract specific feature requests.
Feedback items:
%s

Format your response as a simple bullet-point list of unique, specific features requested.
Each bullet point should be a clear, concise feature description.
Do not include any analysis, headers, or additional commentary.
Example format:
- Feature description 1
- Feature description 2`, strings.Join(feedbacks, "\n"))

	case "improvements":
		prompt = fmt.Sprintf(`You are a product insights analyst.
Task: Analyze these user feedback items about potential improvements.
Feedback items:
%s

Provide a 2-3 sentence summary focusing ONLY on:
1. What specific aspects users want improved
2. Why these improvements matter to users

Be concise and specific. Do not include any analysis headers or bullet points.`, strings.Join(feedbacks, "\n"))

	case "complaints":
		prompt = fmt.Sprintf(`You are a product insights analyst.
Task: Analyze these user complaints and identify core issues.
Feedback items:
%s

Provide a 2-3 sentence summary focusing ONLY on:
1. The main problems users are experiencing
2. Any specific error patterns or technical issues mentioned

Be concise and specific. Do not include any analysis headers or bullet points.`, strings.Join(feedbacks, "\n"))
	}

	// Use the existing LLM call mechanism but modify for summarization
	data := map[string]interface{}{
		"model": chatModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", chatAPI, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Handle non-200 status codes
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no classification result")
	}

	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

type insightTask struct {
	name        string
	insightType string
	feedbacks   []string
	result      InsightSummary
	err         error
}

func (s *FeedbackService) GetTopInsights() (*InsightsResponse, error) {
	response := &InsightsResponse{}

	// Create error channel for parallel processing
	errChan := make(chan error, 2)

	// Process LOB and category insights in parallel
	go func() {
		errChan <- s.processLOBInsights(response)
	}()

	go func() {
		errChan <- s.processCategoryInsights(response)
	}()

	// Wait for both goroutines to complete
	for i := 0; i < 2; i++ {
		if err := <-errChan; err != nil {
			return nil, err
		}
	}

	return response, nil
}

func (s *FeedbackService) processLOBInsights(response *InsightsResponse) error {
	// Optimize the metrics query with better indexing hints
	metricsQuery := `
		SELECT 
			f.lob as name,
			f.insight_type,
			COUNT(*) as feedback_count,
			COUNT(DISTINCT f.username) as user_count,
			COALESCE(AVG(CASE WHEN ua.weight IS NOT NULL THEN ua.weight ELSE 1.0 END), 1.0) as avg_weight
		FROM feedback_insights f
		LEFT JOIN user_affinities ua ON 
			f.category = ua.category AND
			f.lob = ua.lob
		WHERE f.lob != '' 
			AND f.insight_type IN ('feature-request', 'improvement', 'complaint')
		GROUP BY f.lob, f.insight_type
		HAVING COUNT(*) >= 5
		ORDER BY feedback_count DESC
		LIMIT 15
	`

	rows, err := s.db.Raw(metricsQuery).Rows()
	if err != nil {
		return fmt.Errorf("failed to query LOB metrics: %w", err)
	}
	defer rows.Close()

	// Create channels for parallel processing
	taskChan := make(chan insightTask, maxConcurrentLLMCalls)
	resultChan := make(chan insightTask, maxConcurrentLLMCalls)

	// Start worker pool for LLM processing
	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentLLMCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				summary, err := s.processInsightBatch(task.feedbacks, task.insightType)
				task.result = summary
				task.err = err
				resultChan <- task
			}
		}()
	}

	// Create a map to store metrics
	metricsMap := make(map[string]CategoryCount)

	// Process each LOB
	go func() {
		for rows.Next() {
			var item CategoryCount
			var insightType string

			if err := rows.Scan(&item.Name, &insightType, &item.Count, &item.UserCount, &item.AvgWeight); err != nil {
				log.Printf("Error scanning LOB metrics: %v", err)
				continue
			}

			// Store metrics in map
			metricsMap[item.Name] = item

			// Get high-affinity feedback efficiently
			feedbacks, err := s.getHighAffinityFeedback(item.Name, insightType, "lob")
			if err != nil {
				log.Printf("Error getting feedback for LOB %s: %v", item.Name, err)
				continue
			}

			if len(feedbacks) > 0 {
				taskChan <- insightTask{
					name:        item.Name,
					insightType: insightType,
					feedbacks:   feedbacks,
				}
			}
		}
		close(taskChan)
	}()

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Process results
	for result := range resultChan {
		if result.err != nil {
			log.Printf("Error processing insights for %s: %v", result.name, result.err)
			continue
		}

		// Get the original metrics
		metrics, exists := metricsMap[result.name]
		if !exists {
			log.Printf("Warning: metrics not found for %s", result.name)
			continue
		}

		item := CategoryCount{
			Name:      result.name,
			Count:     metrics.Count,
			UserCount: metrics.UserCount,
			AvgWeight: metrics.AvgWeight,
			Summary:   result.result,
		}

		switch result.insightType {
		case "feature-request":
			response.FeatureRequests.TopLOBs = append(response.FeatureRequests.TopLOBs, item)
		case "improvement":
			response.Improvements.TopLOBs = append(response.Improvements.TopLOBs, item)
		case "complaint":
			response.Complaints.TopLOBs = append(response.Complaints.TopLOBs, item)
		}
	}

	return nil
}

func (s *FeedbackService) getHighAffinityFeedback(name, insightType, entityType string) ([]string, error) {
	var query string
	if entityType == "lob" {
		query = `
			SELECT f.feedback
			FROM feedback_insights f
			LEFT JOIN user_affinities ua ON 
				f.category = ua.category AND
				f.lob = ua.lob
			WHERE f.lob = $1
				AND f.insight_type = $2
			ORDER BY COALESCE(ua.weight, 1.0) DESC
			LIMIT $3
		`
	} else {
		query = `
			SELECT f.feedback
			FROM feedback_insights f
			LEFT JOIN user_affinities ua ON 
				f.category = ua.category
			WHERE f.category = $1
				AND f.insight_type = $2
			ORDER BY COALESCE(ua.weight, 1.0) DESC
			LIMIT $3
		`
	}

	var feedbacks []string
	if err := s.db.Raw(query, name, insightType, maxFeedbacksPerBatch).Scan(&feedbacks).Error; err != nil {
		return nil, err
	}
	return feedbacks, nil
}

func (s *FeedbackService) processInsightBatch(feedbacks []string, insightType string) (InsightSummary, error) {
	var summary InsightSummary

	// Add timeout context
	ctx, cancel := context.WithTimeout(context.Background(), llmTimeout)
	defer cancel()

	switch insightType {
	case "feature-request":
		features, err := s.summarizeWithLLMContext(ctx, feedbacks, "feature requests")
		if err != nil {
			return summary, err
		}
		summary.RequestedFeatures = cleanFeatureList(features)
	case "improvement":
		impSummary, err := s.summarizeWithLLMContext(ctx, feedbacks, "improvements")
		if err != nil {
			return summary, err
		}
		summary.ImprovementSummary = strings.TrimSpace(impSummary)
	case "complaint":
		compSummary, err := s.summarizeWithLLMContext(ctx, feedbacks, "complaints")
		if err != nil {
			return summary, err
		}
		summary.ComplaintSummary = strings.TrimSpace(compSummary)
	}

	return summary, nil
}

func (s *FeedbackService) summarizeWithLLMContext(ctx context.Context, feedbacks []string, insightType string) (string, error) {
	prompt := createPrompt(feedbacks, insightType)
	if prompt == "" {
		return "", fmt.Errorf("invalid insight type: %s", insightType)
	}

	// Make API call with context
	data := map[string]interface{}{
		"model": chatModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	body, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", chatAPI, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: llmTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Process response with timeout
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no summary generated")
	}

	return result.Choices[0].Message.Content, nil
}

func cleanFeatureList(features string) []string {
	var cleaned []string
	for _, line := range strings.Split(features, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "• ")
		line = strings.TrimPrefix(line, "* ")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return cleaned
}

func createPrompt(feedbacks []string, insightType string) string {
	switch insightType {
	case "feature requests":
		return fmt.Sprintf(`You are a product insights analyst.
Task: Analyze these user feedback items and extract specific feature requests.
Feedback items:
%s

Format your response as a simple bullet-point list of unique, specific features requested.
Each bullet point should be a clear, concise feature description.
Do not include any analysis, headers, or additional commentary.
Example format:
- Feature description 1
- Feature description 2`, strings.Join(feedbacks, "\n"))

	case "improvements":
		return fmt.Sprintf(`You are a product insights analyst.
Task: Analyze these user feedback items about potential improvements.
Feedback items:
%s

Provide a 2-3 sentence summary focusing ONLY on:
1. What specific aspects users want improved
2. Why these improvements matter to users

Be concise and specific. Do not include any analysis headers or bullet points.`, strings.Join(feedbacks, "\n"))

	case "complaints":
		return fmt.Sprintf(`You are a product insights analyst.
Task: Analyze these user complaints and identify core issues.
Feedback items:
%s

Provide a 2-3 sentence summary focusing ONLY on:
1. The main problems users are experiencing
2. Any specific error patterns or technical issues mentioned

Be concise and specific. Do not include any analysis headers or bullet points.`, strings.Join(feedbacks, "\n"))
	default:
		return ""
	}
}

func (s *FeedbackService) processCategoryInsights(response *InsightsResponse) error {
	metricsQuery := `
		SELECT 
			f.category as name,
			f.insight_type,
			COUNT(*) as feedback_count,
			COUNT(DISTINCT f.username) as user_count,
			COALESCE(AVG(CASE WHEN ua.weight IS NOT NULL THEN ua.weight ELSE 1.0 END), 1.0) as avg_weight
		FROM feedback_insights f
		LEFT JOIN user_affinities ua ON 
			f.category = ua.category
		WHERE f.category != '' 
			AND f.insight_type IN ('feature-request', 'improvement', 'complaint')
		GROUP BY f.category, f.insight_type
		HAVING COUNT(*) >= 3
		ORDER BY feedback_count DESC
		LIMIT 15
	`

	rows, err := s.db.Raw(metricsQuery).Rows()
	if err != nil {
		return fmt.Errorf("failed to query category metrics: %w", err)
	}
	defer rows.Close()

	// Create channels for parallel processing
	taskChan := make(chan insightTask, maxConcurrentLLMCalls)
	resultChan := make(chan insightTask, maxConcurrentLLMCalls)

	// Start worker pool for LLM processing
	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentLLMCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				summary, err := s.processInsightBatch(task.feedbacks, task.insightType)
				task.result = summary
				task.err = err
				resultChan <- task
			}
		}()
	}

	// Create a map to store metrics
	metricsMap := make(map[string]CategoryCount)

	// Process each category
	go func() {
		for rows.Next() {
			var item CategoryCount
			var insightType string

			if err := rows.Scan(&item.Name, &insightType, &item.Count, &item.UserCount, &item.AvgWeight); err != nil {
				log.Printf("Error scanning category metrics: %v", err)
				continue
			}

			// Store metrics in map
			metricsMap[item.Name] = item

			// Get high-affinity feedback efficiently
			feedbacks, err := s.getHighAffinityFeedback(item.Name, insightType, "category")
			if err != nil {
				log.Printf("Error getting feedback for category %s: %v", item.Name, err)
				continue
			}

			if len(feedbacks) > 0 {
				taskChan <- insightTask{
					name:        item.Name,
					insightType: insightType,
					feedbacks:   feedbacks,
				}
			}
		}
		close(taskChan)
	}()

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Process results
	for result := range resultChan {
		if result.err != nil {
			log.Printf("Error processing insights for category %s: %v", result.name, result.err)
			continue
		}

		// Get the original metrics
		metrics, exists := metricsMap[result.name]
		if !exists {
			log.Printf("Warning: metrics not found for %s", result.name)
			continue
		}

		item := CategoryCount{
			Name:      result.name,
			Count:     metrics.Count,
			UserCount: metrics.UserCount,
			AvgWeight: metrics.AvgWeight,
			Summary:   result.result,
		}

		switch result.insightType {
		case "feature-request":
			response.FeatureRequests.TopCategories = append(response.FeatureRequests.TopCategories, item)
		case "improvement":
			response.Improvements.TopCategories = append(response.Improvements.TopCategories, item)
		case "complaint":
			response.Complaints.TopCategories = append(response.Complaints.TopCategories, item)
		}
	}

	return nil
}

func main() {
	log.Println("🚀 Starting Twitter data fetching process")
	log.Printf("📋 Will process %d Twitter handles: %v", len(twitterHandles), twitterHandles)

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
	feedbackService := NewFeedbackService(database)

	// Initialize Gin router
	r := gin.Default()

	// Register routes
	r.GET("/api/v1/feedback", func(c *gin.Context) {
		lob := c.Query("lob")
		if lob == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "lob is required"})
			return
		}

		category := c.Query("category")
		subCategory := c.Query("sub_category")

		resp, err := feedbackService.GetTopFeedback(lob, category, subCategory)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, resp)
	})

	r.GET("/api/v1/feedback/insights", func(c *gin.Context) {
		resp, err := feedbackService.GetTopInsights()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, resp)
	})

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
