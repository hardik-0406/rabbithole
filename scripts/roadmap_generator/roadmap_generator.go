package roadmap_generator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	embedAPI              = "https://api.rabbithole.cred.club/v1/embeddings"
	chatAPI               = "https://api.rabbithole.cred.club/v1/chat/completions"
	apiKey                = "sk-G_BXXmoaRnY5pkImc2yjDw"
	maxItemsPerCategory   = 10
	chatModel             = "claude-3-7-sonnet"
	maxTokens             = 2000
	linearGraphQLEndpoint = "https://api.linear.app/graphql"
	linearAPIKey          = "lin_api_4nhyJjjFfVRW483gOHmQfjFPGHwkGMVFhfXNjVv3"
	teamID                = "b35ab586-6384-493d-b8cf-6efd075ff984"
)

type RoadmapService struct {
	db          *gorm.DB
	ticketCache struct {
		sync.RWMutex
		tickets   []LinearTicket
		lastFetch time.Time
	}
}

type RoadmapItem struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Impact      struct {
		UserCount  int     `json:"user_count"`
		Frequency  int     `json:"frequency"`
		Importance float64 `json:"importance_score"`
	} `json:"impact"`
	RelatedTickets []TicketInfo `json:"related_tickets"`
	Category       string       `json:"category"`
	Priority       string       `json:"priority"` // P0, P1, P2
}

type TicketInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Status string `json:"status"`
}

type RoadmapResponse struct {
	LOB             string `json:"lob"`
	Category        string `json:"category,omitempty"`
	GeneratedAt     string `json:"generated_at"`
	FeatureRequests struct {
		High   []RoadmapItem `json:"high_priority"`
		Medium []RoadmapItem `json:"medium_priority"`
		Low    []RoadmapItem `json:"low_priority"`
	} `json:"feature_requests"`
	Improvements struct {
		High   []RoadmapItem `json:"high_priority"`
		Medium []RoadmapItem `json:"medium_priority"`
		Low    []RoadmapItem `json:"low_priority"`
	} `json:"improvements"`
	Bugs struct {
		High   []RoadmapItem `json:"high_priority"`
		Medium []RoadmapItem `json:"medium_priority"`
		Low    []RoadmapItem `json:"low_priority"`
	} `json:"bugs"`
	Summary struct {
		TotalIssues    int `json:"total_issues"`
		HighPriority   int `json:"high_priority"`
		MediumPriority int `json:"medium_priority"`
		LowPriority    int `json:"low_priority"`
		RelatedTickets int `json:"related_tickets"`
		ImpactedUsers  int `json:"impacted_users"`
	} `json:"summary"`
}

type LLMRequest struct {
	Model       string       `json:"model"`
	Messages    []LLMMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens"`
	Temperature float64      `json:"temperature"`
}

type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type FeedbackData struct {
	Feedback    string  `json:"feedback"`
	InsightType string  `json:"insight_type"`
	Category    string  `json:"category"`
	UserCount   int     `json:"user_count"`
	Frequency   int     `json:"frequency"`
	Importance  float64 `json:"importance"`
}

type LinearTicket struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       struct {
		Name string `json:"name"`
	} `json:"state"`
}

type LinearIssuesResponse struct {
	Data struct {
		Issues struct {
			Nodes []LinearTicket `json:"nodes"`
		} `json:"issues"`
	} `json:"data"`
}

func NewRoadmapService(db *gorm.DB) (*RoadmapService, error) {
	return &RoadmapService{
		db: db,
	}, nil
}

func (s *RoadmapService) GenerateRoadmap(ctx context.Context, lob, category string) (*RoadmapResponse, error) {
	log.Printf("🗺️ Generating roadmap for LOB: %s, Category: %s", lob, category)

	response := &RoadmapResponse{
		LOB:         lob,
		Category:    category,
		GeneratedAt: time.Now().Format(time.RFC3339),
	}

	// Fetch feedback data
	feedbackData, err := s.getFeedbackData(lob, category)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feedback data: %w", err)
	}

	// Generate roadmap items using LLM
	roadmapItems, err := s.generateRoadmapItemsWithLLM(ctx, feedbackData)
	if err != nil {
		return nil, fmt.Errorf("failed to generate roadmap items: %w", err)
	}

	// Find related tickets using LLM
	err = s.findRelatedTickets(ctx, roadmapItems)
	if err != nil {
		log.Printf("Warning: error finding related tickets: %v", err)
	}

	// Organize items by type and priority
	for _, item := range roadmapItems {
		switch item.Type {
		case "feature-request":
			response.FeatureRequests = appendByPriority(response.FeatureRequests, item)
		case "improvement":
			response.Improvements = appendByPriority(response.Improvements, item)
		case "bug":
			response.Bugs = appendByPriority(response.Bugs, item)
		}

		// Update summary metrics
		response.Summary.TotalIssues++
		response.Summary.ImpactedUsers += item.Impact.UserCount
		response.Summary.RelatedTickets += len(item.RelatedTickets)

		switch item.Priority {
		case "P0":
			response.Summary.HighPriority++
		case "P1":
			response.Summary.MediumPriority++
		case "P2":
			response.Summary.LowPriority++
		}
	}

	// Create the roadmap in Linear
	roadmapIssueID, err := s.createRoadmapInLinear(ctx, response)
	if err != nil {
		log.Printf("Warning: failed to create roadmap in Linear: %v", err)
	} else {
		log.Printf("Created roadmap issue in Linear: %s", roadmapIssueID)
	}

	return response, nil
}

func (s *RoadmapService) getFeedbackData(lob, category string) ([]FeedbackData, error) {
	query := `
        SELECT 
            f.feedback,
            f.insight_type,
            f.category,
            COUNT(DISTINCT f.username) as user_count,
            COUNT(*) as frequency,
            AVG(COALESCE(ua.weight, 1.0)) as importance
        FROM feedback_insights f
        LEFT JOIN user_affinities ua ON 
            f.category = ua.category AND
            f.lob = ua.lob
        WHERE f.lob = ?
            AND (? = '' OR f.category = ?)
        GROUP BY f.feedback, f.insight_type, f.category
        HAVING COUNT(*) >= 3
    `

	var feedbackData []FeedbackData
	err := s.db.Raw(query, lob, category, category).Scan(&feedbackData).Error
	return feedbackData, err
}

func (s *RoadmapService) generateRoadmapItemsWithLLM(ctx context.Context, feedbackData []FeedbackData) ([]RoadmapItem, error) {
	if len(feedbackData) == 0 {
		return []RoadmapItem{}, nil
	}

	// Create a more structured prompt to ensure valid JSON response
	prompt := fmt.Sprintf(`Analyze the following user feedback and generate a product roadmap.

Rules:
1. Each roadmap item must have all required fields
2. Priority must be exactly "P0", "P1", or "P2"
3. Type must be exactly "feature-request", "improvement", or "bug"
4. Return a valid JSON array only

Feedback data:
%s

Return a JSON array of roadmap items with this exact structure:
{
  "title": "Brief title",
  "description": "Detailed description",
  "type": "feature-request|improvement|bug",
  "priority": "P0|P1|P2",
  "impact": {
    "user_count": <number>,
    "frequency": <number>,
    "importance_score": <number>
  },
  "category": "string"
}

Return ONLY the JSON array with no additional text.`, formatFeedbackForPrompt(feedbackData))

	response, err := s.callLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	// Clean the response to ensure it's valid JSON
	response = cleanJSONResponse(response)

	// Log the cleaned response for debugging
	log.Printf("Cleaned LLM response: %s", response)

	var roadmapItems []RoadmapItem
	if err := json.Unmarshal([]byte(response), &roadmapItems); err != nil {
		// Try to fix common JSON issues
		response = strings.ReplaceAll(response, "\n", "")
		response = strings.ReplaceAll(response, "\r", "")
		response = strings.TrimSpace(response)

		// Try parsing again
		if err := json.Unmarshal([]byte(response), &roadmapItems); err != nil {
			return nil, fmt.Errorf("failed to parse LLM response: %w\nCleaned response: %s", err, response)
		}
	}

	// Process items concurrently
	var wg sync.WaitGroup
	itemsChan := make(chan *RoadmapItem, len(roadmapItems))
	errChan := make(chan error, len(roadmapItems))
	semaphore := make(chan struct{}, 5) // Limit concurrent operations

	for i := range roadmapItems {
		wg.Add(1)
		go func(item RoadmapItem) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore

			if validItem := validateAndFixRoadmapItem(item, feedbackData); validItem != nil {
				itemsChan <- validItem
			}
		}(roadmapItems[i])
	}

	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(itemsChan)
		close(errChan)
	}()

	// Collect results
	var validItems []RoadmapItem
	for item := range itemsChan {
		validItems = append(validItems, *item)
	}

	// Check for errors
	for err := range errChan {
		if err != nil {
			return nil, err
		}
	}

	return validItems, nil
}

func cleanJSONResponse(response string) string {
	// Find the first '[' and last ']' to extract just the JSON array
	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")

	if start == -1 || end == -1 || end <= start {
		// If no array found, try to find object brackets
		start = strings.Index(response, "{")
		end = strings.LastIndex(response, "}")
		if start == -1 || end == -1 || end <= start {
			return "[]"
		}
		// Wrap single object in array
		return fmt.Sprintf("[%s]", response[start:end+1])
	}

	return response[start : end+1]
}

func validateAndFixRoadmapItem(item RoadmapItem, feedbackData []FeedbackData) *RoadmapItem {
	// Validate required fields
	if item.Title == "" || item.Description == "" {
		return nil
	}

	// Validate and fix type
	switch item.Type {
	case "feature-request", "improvement", "bug":
		// Valid type
	default:
		// Try to infer type from feedback data
		item.Type = inferType(item.Description)
	}

	// Validate and fix priority
	switch item.Priority {
	case "P0", "P1", "P2":
		// Valid priority
	default:
		// Calculate priority based on impact
		item.Priority = calculatePriority(item.Impact.UserCount, item.Impact.Frequency, item.Impact.Importance)
	}

	// Ensure impact values are positive
	if item.Impact.UserCount <= 0 {
		item.Impact.UserCount = 1
	}
	if item.Impact.Frequency <= 0 {
		item.Impact.Frequency = 1
	}
	if item.Impact.Importance <= 0 {
		item.Impact.Importance = 1.0
	}

	return &item
}

func inferType(description string) string {
	description = strings.ToLower(description)

	if strings.Contains(description, "bug") ||
		strings.Contains(description, "error") ||
		strings.Contains(description, "crash") ||
		strings.Contains(description, "fix") {
		return "bug"
	}

	if strings.Contains(description, "add") ||
		strings.Contains(description, "new") ||
		strings.Contains(description, "feature") ||
		strings.Contains(description, "implement") {
		return "feature-request"
	}

	return "improvement"
}

func calculatePriority(userCount, frequency int, importance float64) string {
	score := float64(userCount*3+frequency) * importance

	switch {
	case score > 100:
		return "P0"
	case score > 50:
		return "P1"
	default:
		return "P2"
	}
}

func (s *RoadmapService) findRelatedTickets(ctx context.Context, items []RoadmapItem) error {
	tickets, err := s.fetchLinearTickets(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch Linear tickets: %w", err)
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 3) // Limit concurrent LLM calls

	for i := range items {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore

			prompt := fmt.Sprintf(`
				Given this roadmap item:
				Title: %s
				Description: %s

				And these Linear tickets:
				%s

				Return the IDs of the most relevant tickets (max 5) as a JSON array of strings.
				Consider semantic similarity and relevance to the roadmap item.
			`, items[idx].Title, items[idx].Description, formatTicketsForPrompt(tickets))

			response, err := s.callLLM(ctx, prompt)
			if err != nil {
				return // Skip if error
			}

			var ticketIDs []string
			if err := json.Unmarshal([]byte(response), &ticketIDs); err != nil {
				return
			}

			// Convert Linear tickets to TicketInfo
			var relatedTickets []TicketInfo
			for _, id := range ticketIDs {
				for _, ticket := range tickets {
					if ticket.ID == id {
						relatedTickets = append(relatedTickets, TicketInfo{
							ID:     ticket.ID,
							Title:  ticket.Title,
							Status: ticket.State.Name,
							URL:    "https://linear.app/rabbithole-hardik/issue/" + ticket.ID,
						})
						break
					}
				}
			}

			// Use mutex to safely update the items slice
			items[idx].RelatedTickets = relatedTickets
		}(i)
	}

	wg.Wait()

	return nil
}

func (s *RoadmapService) fetchLinearTickets(ctx context.Context) ([]LinearTicket, error) {
	s.ticketCache.RLock()
	if time.Since(s.ticketCache.lastFetch) < 5*time.Minute {
		tickets := s.ticketCache.tickets
		s.ticketCache.RUnlock()
		return tickets, nil
	}
	s.ticketCache.RUnlock()

	s.ticketCache.Lock()
	defer s.ticketCache.Unlock()

	// Double-check after acquiring write lock
	if time.Since(s.ticketCache.lastFetch) < 5*time.Minute {
		return s.ticketCache.tickets, nil
	}

	// Fetch tickets from Linear API
	query := `
        query {
            issues(first: 100) {
                nodes {
                    id
                    title
                    description
                    state {
                        name
                    }
                }
            }
        }
    `

	payload := map[string]interface{}{
		"query": query,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", linearGraphQLEndpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", linearAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var linearResp LinearIssuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&linearResp); err != nil {
		return nil, err
	}

	s.ticketCache.tickets = linearResp.Data.Issues.Nodes
	s.ticketCache.lastFetch = time.Now()

	return s.ticketCache.tickets, nil
}

func (s *RoadmapService) callLLM(ctx context.Context, prompt string) (string, error) {
	request := LLMRequest{
		Model:       chatModel,
		MaxTokens:   maxTokens,
		Temperature: 0.7,
		Messages: []LLMMessage{
			{
				Role:    "system",
				Content: "You are a product manager helping to generate a product roadmap based on user feedback. Always return responses in valid JSON format.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", chatAPI, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read the full response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Check if response status is not 200
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API error (status %d): %s", resp.StatusCode, string(body))
	}

	var llmResponse LLMResponse
	if err := json.Unmarshal(body, &llmResponse); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w\nResponse body: %s", err, string(body))
	}

	if len(llmResponse.Choices) == 0 {
		return "", fmt.Errorf("no choices in LLM response: %s", string(body))
	}

	return llmResponse.Choices[0].Message.Content, nil
}

func formatFeedbackForPrompt(feedback []FeedbackData) string {
	var builder strings.Builder
	for _, f := range feedback {
		builder.WriteString(fmt.Sprintf("- Type: %s\n  Feedback: %s\n  Category: %s\n  Users: %d\n  Frequency: %d\n  Importance: %.2f\n\n",
			f.InsightType, f.Feedback, f.Category, f.UserCount, f.Frequency, f.Importance))
	}
	return builder.String()
}

func formatTicketsForPrompt(tickets []LinearTicket) string {
	var builder strings.Builder
	for _, t := range tickets {
		builder.WriteString(fmt.Sprintf("ID: %s\nTitle: %s\nDescription: %s\nStatus: %s\n\n",
			t.ID, t.Title, t.Description, t.State.Name))
	}
	return builder.String()
}

func appendByPriority(items struct {
	High   []RoadmapItem `json:"high_priority"`
	Medium []RoadmapItem `json:"medium_priority"`
	Low    []RoadmapItem `json:"low_priority"`
}, item RoadmapItem) struct {
	High   []RoadmapItem `json:"high_priority"`
	Medium []RoadmapItem `json:"medium_priority"`
	Low    []RoadmapItem `json:"low_priority"`
} {
	switch item.Priority {
	case "P0":
		items.High = append(items.High, item)
	case "P1":
		items.Medium = append(items.Medium, item)
	case "P2":
		items.Low = append(items.Low, item)
	}
	return items
}

func generateTitle(feedback string) string {
	// Truncate and clean the feedback to create a title
	words := strings.Fields(feedback)
	if len(words) > 8 {
		words = words[:8]
	}
	return strings.Join(words, " ") + "..."
}

func categorizePriority(items []RoadmapItem) (high, medium, low []RoadmapItem) {
	for _, item := range items {
		switch item.Priority {
		case "P0":
			high = append(high, item)
		case "P1":
			medium = append(medium, item)
		case "P2":
			low = append(low, item)
		}
	}

	// Sort each category by impact
	sortByImpact := func(items []RoadmapItem) {
		sort.Slice(items, func(i, j int) bool {
			scoreI := float64(items[i].Impact.UserCount) * items[i].Impact.Importance
			scoreJ := float64(items[j].Impact.UserCount) * items[j].Impact.Importance
			return scoreI > scoreJ
		})
	}

	sortByImpact(high)
	sortByImpact(medium)
	sortByImpact(low)

	return
}

func (s *RoadmapService) createRoadmapInLinear(ctx context.Context, roadmap *RoadmapResponse) (string, error) {
	// Create a formatted markdown description
	description := fmt.Sprintf(`# Product Roadmap: %s
Generated on: %s

## 🚨 High Priority Issues (P0)

### Critical Bugs
%s

### High Priority Features
%s

## 🔄 Medium Priority Issues (P1)

### Features & Improvements
%s

## 📋 Low Priority Issues (P2)

### Features & Improvements
%s

## 📊 Impact Summary
- Total Issues: %d
- High Priority: %d
- Medium Priority: %d
- Low Priority: %d
- Total Impacted Users: %d

## 📈 Implementation Timeline
- **Immediate (1-2 weeks)**: Address P0 bugs affecting payment processing and transaction reliability
- **Short-term (1-2 months)**: Implement medium priority features for payment management and analytics
- **Long-term (3-6 months)**: Roll out low priority improvements and additional features

## 🎯 Success Metrics
1. Reduce payment-related errors by 95%
2. Improve user satisfaction scores by 30%
3. Increase feature adoption rate by 40%
4. Decrease support tickets related to payments by 60%`,
		roadmap.LOB,
		roadmap.GeneratedAt,
		formatHighPriorityBugs(roadmap.Bugs.High),
		formatHighPriorityFeatures(roadmap.FeatureRequests.High),
		formatMediumPriorityItems(roadmap.FeatureRequests.Medium, roadmap.Improvements.Medium),
		formatLowPriorityItems(roadmap.FeatureRequests.Low, roadmap.Improvements.Low),
		roadmap.Summary.TotalIssues,
		roadmap.Summary.HighPriority,
		roadmap.Summary.MediumPriority,
		roadmap.Summary.LowPriority,
		roadmap.Summary.ImpactedUsers,
	)

	// Create the Linear issue
	query := `
        mutation CreateIssue($title: String!, $description: String!, $teamId: String!) {
            issueCreate(
                input: {
                    title: $title,
                    description: $description,
                    teamId: $teamId
                }
            ) {
                success
                issue {
                    id
                }
            }
        }
    `

	variables := map[string]interface{}{
		"title":       fmt.Sprintf("Product Roadmap: %s - %s", roadmap.LOB, time.Now().Format("Jan 2006")),
		"description": description,
		"teamId":      teamID,
	}

	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Log the request payload for debugging
	log.Printf("Linear API Request: %s", string(jsonData))

	req, err := http.NewRequestWithContext(ctx, "POST", linearGraphQLEndpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", linearAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Log the response for debugging
	log.Printf("Linear API Response: %s", string(body))

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Linear API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			IssueCreate struct {
				Success bool `json:"success"`
				Issue   struct {
					ID string `json:"id"`
				} `json:"issue"`
			} `json:"issueCreate"`
		} `json:"data"`
		Errors []struct {
			Message string   `json:"message"`
			Path    []string `json:"path"`
		} `json:"errors"`
	}

	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w\nResponse body: %s", err, string(body))
	}

	// Check for GraphQL errors
	if len(result.Errors) > 0 {
		var errMsgs []string
		for _, err := range result.Errors {
			errMsgs = append(errMsgs, fmt.Sprintf("%s (path: %v)", err.Message, err.Path))
		}
		return "", fmt.Errorf("GraphQL errors: %s", strings.Join(errMsgs, "; "))
	}

	// Check for mutation-specific errors
	if !result.Data.IssueCreate.Success {
		return "", fmt.Errorf("issue creation failed without specific error message")
	}

	return result.Data.IssueCreate.Issue.ID, nil
}

// Helper functions to format the roadmap sections
func formatHighPriorityBugs(bugs []RoadmapItem) string {
	if len(bugs) == 0 {
		return "No critical bugs identified.\n"
	}

	var builder strings.Builder
	for _, bug := range bugs {
		builder.WriteString(fmt.Sprintf(`
#### 🐛 %s
- **Impact**: %d users affected (%d occurrences)
- **Description**: %s
- **Priority**: %s
`,
			bug.Title,
			bug.Impact.UserCount,
			bug.Impact.Frequency,
			bug.Description,
			bug.Priority))
	}
	return builder.String()
}

func formatHighPriorityFeatures(features []RoadmapItem) string {
	if len(features) == 0 {
		return "No high priority features identified.\n"
	}

	var builder strings.Builder
	for _, feature := range features {
		builder.WriteString(fmt.Sprintf(`
#### ✨ %s
- **Impact**: %d users requested (%d mentions)
- **Description**: %s
- **Priority**: %s
`,
			feature.Title,
			feature.Impact.UserCount,
			feature.Impact.Frequency,
			feature.Description,
			feature.Priority))
	}
	return builder.String()
}

func formatMediumPriorityItems(features, improvements []RoadmapItem) string {
	var builder strings.Builder

	if len(features) == 0 && len(improvements) == 0 {
		return "No medium priority items identified.\n"
	}

	for _, item := range append(features, improvements...) {
		var icon string
		if item.Type == "feature-request" {
			icon = "✨"
		} else {
			icon = "⚡"
		}

		builder.WriteString(fmt.Sprintf(`
#### %s %s
- **Impact**: %d users (%d mentions)
- **Description**: %s
- **Type**: %s
`,
			icon,
			item.Title,
			item.Impact.UserCount,
			item.Impact.Frequency,
			item.Description,
			item.Type))
	}
	return builder.String()
}

func formatLowPriorityItems(features, improvements []RoadmapItem) string {
	var builder strings.Builder

	if len(features) == 0 && len(improvements) == 0 {
		return "No low priority items identified.\n"
	}

	for _, item := range append(features, improvements...) {
		var icon string
		if item.Type == "feature-request" {
			icon = "✨"
		} else {
			icon = "⚡"
		}

		builder.WriteString(fmt.Sprintf(`
#### %s %s
- **Impact**: %d users (%d mentions)
- **Description**: %s
- **Type**: %s
`,
			icon,
			item.Title,
			item.Impact.UserCount,
			item.Impact.Frequency,
			item.Description,
			item.Type))
	}
	return builder.String()
}
