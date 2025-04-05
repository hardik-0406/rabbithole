package insights

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"rabbithole/models"
	"rabbithole/secrets"

	"gorm.io/gorm"
)

const (
	chatAPI               = "https://api.rabbithole.cred.club/v1/chat/completions"
	apiKey                = "sk-G_BXXmoaRnY5pkImc2yjDw"
	chatModel             = "claude-3-7-sonnet"
	maxConcurrentLLMCalls = 5
	llmTimeout            = 15 * time.Second
	maxFeedbacksPerBatch  = 10
	maxBatchSize          = 10
	minFeedbackForInsight = 3
)

// Response types
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
	RequestedFeatures  []string `json:"requested_features"`
	ImprovementSummary string   `json:"improvement_summary"`
	ComplaintSummary   string   `json:"complaint_summary"`
}

type CategoryCount struct {
	Name      string         `json:"name"`
	Count     int            `json:"count"`
	UserCount int            `json:"user_count"`
	AvgWeight float64        `json:"avg_weight"`
	Summary   InsightSummary `json:"summary"`
}

type InsightsResponse struct {
	FeatureRequests struct {
		TopLOBs       []CategoryInsight `json:"top_lobs"`
		TopCategories []CategoryInsight `json:"top_categories"`
	} `json:"feature_requests"`
	Improvements struct {
		TopLOBs       []CategoryInsight `json:"top_lobs"`
		TopCategories []CategoryInsight `json:"top_categories"`
	} `json:"improvements"`
	Complaints struct {
		TopLOBs       []CategoryInsight `json:"top_lobs"`
		TopCategories []CategoryInsight `json:"top_categories"`
	} `json:"complaints"`
}

type CategoryInsight struct {
	Name           string         `json:"name"`
	Count          int            `json:"count"`
	ImpactScore    float32        `json:"impact_score"`
	TrendingIssues []string       `json:"trending_issues"`
	Summary        InsightSummary `json:"summary"`
}

type insightTask struct {
	name        string
	insightType string
	feedbacks   []string
	result      InsightSummary
	err         error
}

type InsightResponse struct {
	LOB      string               `json:"lob"`
	Category string               `json:"category,omitempty"`
	Folder   string               `json:"folder,omitempty"`
	Insights map[string][]Insight `json:"insights"` // Mapped by feedback type
	Metrics  MetricsSummary       `json:"metrics"`
}

type Insight struct {
	Summary       string   `json:"summary"`
	ActionItems   []string `json:"action_items"`
	ImpactScore   float32  `json:"impact_score"`
	FeedbackCount int      `json:"feedback_count"`
	Examples      []string `json:"examples"` // Representative feedback examples
}

type MetricsSummary struct {
	TotalFeedback   int                `json:"total_feedback"`
	AvgRating       float32            `json:"avg_rating"`
	TrendingIssues  []string           `json:"trending_issues"`
	TopCategories   []string           `json:"top_categories"`
	ImpactBreakdown map[string]float32 `json:"impact_breakdown"`
}

// Service
type InsightsService struct {
	db          *gorm.DB
	rateLimiter chan struct{}
	llm         *LLMClient
}

func NewInsightsService(db *gorm.DB) (*InsightsService, error) {
	if err := db.AutoMigrate(&models.InsightGroup{}, &models.InsightMetrics{}); err != nil {
		return nil, fmt.Errorf("failed to migrate insights tables: %w", err)
	}

	return &InsightsService{
		db:          db,
		rateLimiter: make(chan struct{}, maxConcurrentLLMCalls),
		llm:         NewLLMClient(secrets.CHAT_API, secrets.API_KEY),
	}, nil
}

// GetTopFeedback returns top feedback items for a given LOB
func (s *InsightsService) GetTopFeedback(ctx context.Context, lob, category, subCategory string) (*FeedbackResponse, error) {
	if lob == "" {
		return nil, fmt.Errorf("lob is required")
	}

	query := `
        WITH feedback_scores AS (
            SELECT 
                f.content as feedback,
                p.feedback_type as insight_type,
                COUNT(*) as feedback_count,
                AVG(COALESCE(f.rating, 3)) as avg_rating,
                COUNT(*) * AVG(COALESCE(f.rating, 3)) as weighted_score
            FROM feedbacks f
            JOIN predictions p ON f.id = p.feedback_id
            WHERE p.lob = $1
            GROUP BY f.content, p.feedback_type
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
			FeedbackCount int
			AvgRating     float64
			WeightedScore float64
		}
		if err := rows.Scan(&item.Feedback, &item.InsightType, &item.FeedbackCount, &item.AvgRating, &item.WeightedScore); err != nil {
			return nil, fmt.Errorf("failed to scan feedback: %w", err)
		}

		feedbackItem := FeedbackItem{
			Feedback:    item.Feedback,
			UserCount:   item.FeedbackCount,
			Score:       item.WeightedScore,
			AffinityAvg: item.AvgRating,
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

// GetTopInsights returns aggregated insights across all feedback
func (s *InsightsService) GetTopInsights(ctx context.Context) (*InsightsResponse, error) {
	response := &InsightsResponse{
		FeatureRequests: struct {
			TopLOBs       []CategoryInsight `json:"top_lobs"`
			TopCategories []CategoryInsight `json:"top_categories"`
		}{
			TopLOBs:       make([]CategoryInsight, 0),
			TopCategories: make([]CategoryInsight, 0),
		},
		Improvements: struct {
			TopLOBs       []CategoryInsight `json:"top_lobs"`
			TopCategories []CategoryInsight `json:"top_categories"`
		}{
			TopLOBs:       make([]CategoryInsight, 0),
			TopCategories: make([]CategoryInsight, 0),
		},
		Complaints: struct {
			TopLOBs       []CategoryInsight `json:"top_lobs"`
			TopCategories []CategoryInsight `json:"top_categories"`
		}{
			TopLOBs:       make([]CategoryInsight, 0),
			TopCategories: make([]CategoryInsight, 0),
		},
	}

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

// Keep all the existing helper methods (getTop5, processLOBInsights, processCategoryInsights, etc.)
// ... (copy all the helper methods from the main.go file)

func getTop5(items []FeedbackItem) []FeedbackItem {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})

	if len(items) > 5 {
		return items[:5]
	}
	return items
}

func (s *InsightsService) processLOBInsights(response *InsightsResponse) error {
	metricsQuery := `
        WITH feedback_metrics AS (
            SELECT 
                p.lob as name,
                p.feedback_type,
                COUNT(*) as feedback_count,
                COUNT(DISTINCT f.id) as unique_count,
                AVG(COALESCE(f.rating, 3)) as avg_rating,
                SUM(CASE 
                    WHEN f.posted_at >= NOW() - INTERVAL '30 days' THEN 2
                    WHEN f.posted_at >= NOW() - INTERVAL '90 days' THEN 1.5
                    ELSE 1
                END) as recency_score
            FROM feedbacks f
            JOIN predictions p ON f.id = p.feedback_id
            WHERE p.lob != '' 
                AND p.feedback_type IN ('feature-request', 'improvement', 'complaint')
            GROUP BY p.lob, p.feedback_type
            HAVING COUNT(*) >= 5
        )
        SELECT 
            name,
            feedback_type,
            feedback_count,
            unique_count,
            avg_rating,
            (feedback_count * avg_rating * recency_score) as impact_score
        FROM feedback_metrics
        ORDER BY impact_score DESC
        LIMIT 15
    `

	rows, err := s.db.Raw(metricsQuery).Rows()
	if err != nil {
		return fmt.Errorf("failed to query LOB metrics: %w", err)
	}
	defer rows.Close()

	type metricRow struct {
		Name         string
		FeedbackType string
		Count        int
		UniqueCount  int
		AvgRating    float64
		ImpactScore  float64
	}

	taskChan := make(chan insightTask, maxConcurrentLLMCalls)
	resultChan := make(chan insightTask, maxConcurrentLLMCalls)

	// Start worker pool
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

	metricsMap := make(map[string]metricRow)

	go func() {
		for rows.Next() {
			var row metricRow
			if err := rows.Scan(
				&row.Name,
				&row.FeedbackType,
				&row.Count,
				&row.UniqueCount,
				&row.AvgRating,
				&row.ImpactScore,
			); err != nil {
				log.Printf("Error scanning LOB metrics: %v", err)
				continue
			}

			metricsMap[row.Name] = row

			feedbacks, err := s.getHighAffinityFeedback(row.Name, row.FeedbackType, "lob")
			if err != nil {
				log.Printf("Error getting feedback for LOB %s: %v", row.Name, err)
				continue
			}

			if len(feedbacks) > 0 {
				taskChan <- insightTask{
					name:        row.Name,
					insightType: row.FeedbackType,
					feedbacks:   feedbacks,
				}
			}
		}
		close(taskChan)
	}()

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		if result.err != nil {
			log.Printf("Error processing insights for %s: %v", result.name, result.err)
			continue
		}

		metrics, exists := metricsMap[result.name]
		if !exists {
			log.Printf("Warning: metrics not found for %s", result.name)
			continue
		}

		item := CategoryInsight{
			Name:           result.name,
			Count:          metrics.Count,
			ImpactScore:    float32(metrics.ImpactScore),
			TrendingIssues: make([]string, 0),
			Summary:        result.result,
		}

		switch result.insightType {
		case "feature-request":
			if response.FeatureRequests.TopLOBs == nil {
				response.FeatureRequests.TopLOBs = make([]CategoryInsight, 0)
			}
			response.FeatureRequests.TopLOBs = append(response.FeatureRequests.TopLOBs, item)
		case "improvement":
			if response.Improvements.TopLOBs == nil {
				response.Improvements.TopLOBs = make([]CategoryInsight, 0)
			}
			response.Improvements.TopLOBs = append(response.Improvements.TopLOBs, item)
		case "complaint":
			if response.Complaints.TopLOBs == nil {
				response.Complaints.TopLOBs = make([]CategoryInsight, 0)
			}
			response.Complaints.TopLOBs = append(response.Complaints.TopLOBs, item)
		}
	}

	return nil
}

func (s *InsightsService) getHighAffinityFeedback(name, insightType, entityType string) ([]string, error) {
	var query string
	if entityType == "lob" {
		query = `
            SELECT f.content as feedback
            FROM feedbacks f
            JOIN predictions p ON f.id = p.feedback_id
            WHERE p.lob = $1
                AND p.feedback_type = $2
            ORDER BY f.rating DESC NULLS LAST, f.posted_at DESC
            LIMIT $3
        `
	} else {
		query = `
            SELECT f.content as feedback
            FROM feedbacks f
            JOIN predictions p ON f.id = p.feedback_id
            WHERE p.category = $1
                AND p.feedback_type = $2
            ORDER BY f.rating DESC NULLS LAST, f.posted_at DESC
            LIMIT $3
        `
	}

	var feedbacks []string
	if err := s.db.Raw(query, name, insightType, maxFeedbacksPerBatch).Scan(&feedbacks).Error; err != nil {
		return nil, err
	}
	return feedbacks, nil
}

func (s *InsightsService) processInsightBatch(feedbacks []string, insightType string) (InsightSummary, error) {
	summary := InsightSummary{
		RequestedFeatures:  make([]string, 0),
		ImprovementSummary: "",
		ComplaintSummary:   "",
	}

	if len(feedbacks) == 0 {
		return summary, nil
	}

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), llmTimeout)
	defer cancel()

	// Add rate limiting
	s.rateLimiter <- struct{}{}
	defer func() { <-s.rateLimiter }()

	var err error
	switch insightType {
	case "feature-request":
		var result string
		result, err = s.llm.GenerateInsightSummary(ctx, feedbacks, insightType)
		if err == nil {
			summary.RequestedFeatures = cleanFeatureList(result)
		}
	case "improvement":
		summary.ImprovementSummary, err = s.llm.GenerateInsightSummary(ctx, feedbacks, insightType)
	case "complaint":
		summary.ComplaintSummary, err = s.llm.GenerateInsightSummary(ctx, feedbacks, insightType)
	}

	if err != nil {
		// Log error but return partial results if available
		log.Printf("Error processing %s insights: %v", insightType, err)
		return summary, nil
	}

	return summary, nil
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
	feedbackText := strings.Join(feedbacks, "\n")

	switch insightType {
	case "feature-request":
		return fmt.Sprintf(`Analyze these user feedback items and extract feature requests:

%s

RESPONSE FORMAT:
- List only the specific features requested
- Start each line with a hyphen
- No headers, no explanations
- Maximum 5 features
- Be concise and specific`, feedbackText)

	case "improvement":
		return fmt.Sprintf(`Analyze these improvement suggestions:

%s

RESPONSE FORMAT:
Write 2-3 plain sentences that:
1. State what users want improved
2. Explain the impact on user experience
3. No formatting, no bullet points, no headers`, feedbackText)

	case "complaint":
		return fmt.Sprintf(`Analyze these user complaints:

%s

RESPONSE FORMAT:
Write 3-4 plain sentences that:
1. State the main problems
2. Include specific numbers or percentages
3. Mention business impact
4. No formatting, no bullet points, no headers`, feedbackText)
	}
	return ""
}

// Add a helper function to clean up LLM responses
func cleanLLMResponse(response, insightType string) string {
	// Remove any markdown formatting
	response = strings.ReplaceAll(response, "#", "")
	response = strings.ReplaceAll(response, "**", "")
	response = strings.ReplaceAll(response, "*", "")
	response = strings.ReplaceAll(response, "`", "")
	response = strings.ReplaceAll(response, "|", "")
	response = strings.ReplaceAll(response, "---", "")

	// Remove any headers or sections
	lines := strings.Split(response, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip empty lines and lines that look like headers
		if line == "" || strings.HasPrefix(line, "##") || strings.HasPrefix(line, "###") {
			continue
		}
		cleaned = append(cleaned, line)
	}

	return strings.TrimSpace(strings.Join(cleaned, " "))
}

// Update the LLM client to use the cleanup function
func (c *LLMClient) GenerateInsightSummary(ctx context.Context, feedbacks []string, feedbackType string) (string, error) {
	prompt := createPrompt(feedbacks, feedbackType)
	response, err := c.callLLM(ctx, prompt)
	if err != nil {
		return "", err
	}
	return cleanLLMResponse(response, feedbackType), nil
}

func (s *InsightsService) processCategoryInsights(response *InsightsResponse) error {
	metricsQuery := `
		SELECT 
			p.category as name,
			p.feedback_type as insight_type,
			COUNT(*) as feedback_count,
			COUNT(DISTINCT f.id) as unique_count,
			AVG(COALESCE(f.rating, 3)) as avg_rating
		FROM feedbacks f
		JOIN predictions p ON f.id = p.feedback_id
		WHERE p.category != '' 
			AND p.feedback_type IN ('feature-request', 'improvement', 'complaint')
		GROUP BY p.category, p.feedback_type
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

		metrics, exists := metricsMap[result.name]
		if !exists {
			log.Printf("Warning: metrics not found for %s", result.name)
			continue
		}

		item := CategoryInsight{
			Name:           result.name,
			Count:          metrics.Count,
			ImpactScore:    float32(metrics.AvgWeight),
			TrendingIssues: make([]string, 0),
			Summary:        result.result,
		}

		switch result.insightType {
		case "feature-request":
			if response.FeatureRequests.TopCategories == nil {
				response.FeatureRequests.TopCategories = make([]CategoryInsight, 0)
			}
			response.FeatureRequests.TopCategories = append(response.FeatureRequests.TopCategories, item)
		case "improvement":
			if response.Improvements.TopCategories == nil {
				response.Improvements.TopCategories = make([]CategoryInsight, 0)
			}
			response.Improvements.TopCategories = append(response.Improvements.TopCategories, item)
		case "complaint":
			if response.Complaints.TopCategories == nil {
				response.Complaints.TopCategories = make([]CategoryInsight, 0)
			}
			response.Complaints.TopCategories = append(response.Complaints.TopCategories, item)
		}
	}

	return nil
}

// GenerateInsights creates insights for a specific LOB/category
func (s *InsightsService) GenerateInsights(ctx context.Context, lob, category, folder string) (*InsightResponse, error) {
	// 1. Get grouped feedback by type
	feedbackGroups, err := s.getFeedbackGroups(lob, category, folder)
	if err != nil {
		return nil, err
	}

	// 2. Process each group in parallel
	var wg sync.WaitGroup
	insights := make(map[string][]Insight)
	errCh := make(chan error, len(feedbackGroups))

	for feedbackType, feedbacks := range feedbackGroups {
		wg.Add(1)
		go func(ft string, fbs []models.Feedback) {
			defer wg.Done()

			// Process feedback in batches
			batches := createFeedbackBatches(fbs, maxBatchSize)
			typeInsights := make([]Insight, 0)

			for _, batch := range batches {
				var contents []string
				for _, f := range batch {
					contents = append(contents, f.Content)
				}

				insight, err := s.processInsightBatch(contents, ft)
				if err != nil {
					errCh <- err
					return
				}

				// Convert InsightSummary to Insight
				processedInsight := Insight{
					Summary:       insight.ImprovementSummary,
					ActionItems:   insight.RequestedFeatures,
					ImpactScore:   calculateImpactScore(batch),
					FeedbackCount: len(batch),
					Examples:      selectRepresentativeExamples(batch),
				}

				if ft == "complaint" {
					processedInsight.Summary = insight.ComplaintSummary
				}

				typeInsights = append(typeInsights, processedInsight)
			}

			insights[ft] = typeInsights
		}(feedbackType, feedbacks)
	}

	wg.Wait()
	close(errCh)

	// Check for errors
	for err := range errCh {
		if err != nil {
			return nil, err
		}
	}

	// 3. Calculate metrics
	metrics, err := s.calculateMetrics(feedbackGroups)
	if err != nil {
		return nil, err
	}

	return &InsightResponse{
		LOB:      lob,
		Category: category,
		Folder:   folder,
		Insights: insights,
		Metrics:  metrics,
	}, nil
}

func (s *InsightsService) getFeedbackGroups(lob, category, folder string) (map[string][]models.Feedback, error) {
	query := s.db.Model(&models.Feedback{}).
		Preload("Prediction").
		Joins("JOIN predictions ON feedbacks.id = predictions.feedback_id").
		Where("predictions.feedback_type IS NOT NULL")

	if lob != "" {
		query = query.Where("predictions.lob = ?", lob)
	}
	if category != "" {
		query = query.Where("predictions.category = ?", category)
	}
	if folder != "" {
		query = query.Where("predictions.folder = ?", folder)
	}

	var feedbacks []models.Feedback
	if err := query.Find(&feedbacks).Error; err != nil {
		return nil, err
	}

	groups := make(map[string][]models.Feedback)
	for _, f := range feedbacks {
		groups[f.Prediction.FeedbackType] = append(groups[f.Prediction.FeedbackType], f)
	}

	return groups, nil
}

func calculateImpactScore(feedbacks []models.Feedback) float32 {
	if len(feedbacks) == 0 {
		return 0
	}

	var totalScore float32
	var totalRatings float32
	now := time.Now()

	for _, f := range feedbacks {
		score := float32(1.0)

		if f.Rating > 0 {
			ratingImpact := (6 - f.Rating) / 5
			score *= ratingImpact
			totalRatings++
		}

		daysSince := now.Sub(f.PostedAt).Hours() / 24
		if daysSince <= 30 {
			score *= 1.5 // Recent feedback has higher impact
		}

		totalScore += score
	}

	normalizedScore := totalScore / float32(len(feedbacks))
	if totalRatings > 0 {
		normalizedScore *= (totalRatings / float32(len(feedbacks)))
	}

	return normalizedScore
}

func selectRepresentativeExamples(feedbacks []models.Feedback) []string {
	if len(feedbacks) <= 3 {
		examples := make([]string, 0, len(feedbacks))
		for _, f := range feedbacks {
			examples = append(examples, f.Content)
		}
		return examples
	}

	// Sort by impact (rating and recency)
	sort.Slice(feedbacks, func(i, j int) bool {
		scoreI := calculateImpactScore([]models.Feedback{feedbacks[i]})
		scoreJ := calculateImpactScore([]models.Feedback{feedbacks[j]})
		return scoreI > scoreJ
	})

	return []string{
		feedbacks[0].Content,
		feedbacks[len(feedbacks)/2].Content,
		feedbacks[len(feedbacks)-1].Content,
	}
}

func createFeedbackBatches(feedbacks []models.Feedback, batchSize int) [][]models.Feedback {
	var batches [][]models.Feedback
	for i := 0; i < len(feedbacks); i += batchSize {
		end := i + batchSize
		if end > len(feedbacks) {
			end = len(feedbacks)
		}
		batches = append(batches, feedbacks[i:end])
	}
	return batches
}

func (s *InsightsService) calculateMetrics(feedbackGroups map[string][]models.Feedback) (MetricsSummary, error) {
	var metrics MetricsSummary
	metrics.ImpactBreakdown = make(map[string]float32)

	var totalFeedback int
	var totalRating float32
	var ratingCount int

	for feedbackType, feedbacks := range feedbackGroups {
		totalFeedback += len(feedbacks)
		impactScore := calculateImpactScore(feedbacks)
		metrics.ImpactBreakdown[feedbackType] = impactScore

		for _, f := range feedbacks {
			if f.Rating > 0 {
				totalRating += f.Rating
				ratingCount++
			}
		}
	}

	metrics.TotalFeedback = totalFeedback
	if ratingCount > 0 {
		metrics.AvgRating = totalRating / float32(ratingCount)
	}

	// Calculate trending issues
	metrics.TrendingIssues = s.identifyTrendingIssues(feedbackGroups)
	metrics.TopCategories = s.identifyTopCategories(feedbackGroups)

	return metrics, nil
}

func (s *InsightsService) identifyTrendingIssues(feedbackGroups map[string][]models.Feedback) []string {
	type issueScore struct {
		issue       string
		score       float32
		count       int
		avgRating   float32
		recentCount int
	}

	issues := make(map[string]issueScore)
	now := time.Now()

	for _, feedbacks := range feedbackGroups {
		for _, f := range feedbacks {
			daysSince := now.Sub(f.PostedAt).Hours() / 24

			key := fmt.Sprintf("%s > %s", f.Prediction.LOB, f.Prediction.Category)
			current := issues[key]
			current.count++

			if f.Rating > 0 {
				current.avgRating = (current.avgRating*float32(current.count-1) + f.Rating) / float32(current.count)
			}

			if daysSince <= 30 {
				current.recentCount++
			}

			// Calculate score based on multiple factors
			current.score = float32(current.count) *
				current.avgRating *
				float32(1+current.recentCount) *
				float32(1+len(feedbacks))

			current.issue = fmt.Sprintf("%s: %s", key, summarizeIssue(feedbacks))
			issues[key] = current
		}
	}

	// Sort and return top issues
	var scores []issueScore
	for _, score := range issues {
		scores = append(scores, score)
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	var trending []string
	for i := 0; i < len(scores) && i < 5; i++ {
		trending = append(trending, scores[i].issue)
	}

	return trending
}

func summarizeIssue(feedbacks []models.Feedback) string {
	// Group similar feedback using basic text similarity
	groups := groupSimilarFeedback(feedbacks)

	// Return the most common issue
	if len(groups) > 0 {
		return groups[0].summary
	}
	return ""
}

func groupSimilarFeedback(feedbacks []models.Feedback) []struct {
	summary string
	count   int
} {
	// Implementation for grouping similar feedback
	// This could use simple word overlap or more sophisticated text similarity
	// For now, returning a simplified version
	return nil
}

func (s *InsightsService) identifyTopCategories(feedbackGroups map[string][]models.Feedback) []string {
	categories := make(map[string]float32)

	for _, feedbacks := range feedbackGroups {
		for _, f := range feedbacks {
			key := fmt.Sprintf("%s > %s", f.Prediction.LOB, f.Prediction.Category)
			impactScore := calculateImpactScore([]models.Feedback{f})
			categories[key] += impactScore
		}
	}

	// Convert to slice and sort
	type categoryScore struct {
		category string
		score    float32
	}

	var scores []categoryScore
	for category, score := range categories {
		scores = append(scores, categoryScore{category, score})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	// Return top 5 categories
	var top []string
	for i := 0; i < len(scores) && i < 5; i++ {
		top = append(top, scores[i].category)
	}

	return top
}
