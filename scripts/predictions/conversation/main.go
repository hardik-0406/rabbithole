package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"rabbithole/db"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

type Conversation struct {
	ID         uint      `gorm:"primaryKey"`
	UserUUID   string    `gorm:"index"`
	AgentID    string    `gorm:"index"`
	Topic      string    `gorm:"index"`
	Transcript string    `gorm:"type:text"`
	Status     string    `gorm:"index"` // open, resolved, escalated
	Rating     int       // 1-5 stars
	CreatedAt  time.Time `gorm:"index"`
}

// TaxonomyEmbeddingResult is used for raw SQL queries
type TaxonomyEmbeddingResult struct {
	ID          uint
	LOB         string
	Category    string
	Subcategory string
	Question    string
	CreatedAt   time.Time
	Distance    float64
}

type TaxonomyEmbedding struct {
	ID        uint
	LOB       string
	Category  string
	Folder    string          // instead of Subcategory
	Title     string          // instead of Question
	Embedding pq.Float32Array `gorm:"type:vector(1536)"`
	CreatedAt time.Time
}

type FeedbackInsight struct {
	ID          uint   `gorm:"primaryKey"`
	OriginalID  uint   `gorm:"index"`
	Source      string `gorm:"index"`
	Username    string
	Feedback    string `gorm:"type:text"`
	LOB         string
	Category    string
	Subcategory string
	Question    string
	InsightType string    `gorm:"index"`
	CreatedAt   time.Time `gorm:"index"`
}

const (
	embedAPI   = "https://api.rabbithole.cred.club/v1/embeddings"
	chatAPI    = "https://api.rabbithole.cred.club/v1/chat/completions"
	apiKey     = "sk-G_BXXmoaRnY5pkImc2yjDw"
	embedModel = "text-embedding-3-small"
	chatModel  = "claude-3-5-sonnet"
)

func main() {
	db, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ DB connection error:", err)
	}

	db.AutoMigrate(&FeedbackInsight{})

	var conversations []Conversation
	db.Find(&conversations)

	for _, conv := range conversations {
		var exists bool
		db.Raw("SELECT EXISTS (SELECT 1 FROM feedback_insights WHERE original_id = ? AND source = 'conversation')", conv.ID).Scan(&exists)
		if exists {
			continue
		}

		// Extract user messages from transcript for better analysis
		userMessages := extractUserMessages(conv.Transcript)
		if len(userMessages) == 0 {
			log.Printf("⚠️ Skipping conversation %d - no user messages found\n", conv.ID)
			continue
		}

		// Combine user messages for analysis
		combinedText := strings.Join(userMessages, " ")

		tax, insightType := categorizeFeedback(combinedText, conv.Topic, conv.Status, conv.Rating, db)
		if tax == nil {
			log.Printf("⚠️ Skipping conversation %d - no taxonomy match\n", conv.ID)
			continue
		}

		insight := FeedbackInsight{
			OriginalID:  conv.ID,
			Source:      "conversation",
			Username:    conv.UserUUID,
			Feedback:    combinedText,
			LOB:         tax.LOB,
			Category:    tax.Category,
			Subcategory: tax.Folder,
			Question:    tax.Title,
			InsightType: insightType,
			CreatedAt:   time.Now(),
		}
		db.Create(&insight)

		log.Printf("✅ Stored: Conversation %d → %s > %s > %s [%s]",
			conv.ID, tax.Category, tax.Folder, tax.Title, insightType)

		time.Sleep(2 * time.Second)
	}
}

func extractUserMessages(transcript string) []string {
	var userMessages []string
	lines := strings.Split(transcript, "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "User:") {
			// Remove "User:" prefix and trim spaces
			message := strings.TrimSpace(strings.TrimPrefix(line, "User:"))
			if message != "" {
				userMessages = append(userMessages, message)
			}
		}
	}

	return userMessages
}

func categorizeFeedback(text, topic, status string, rating int, db *gorm.DB) (*TaxonomyEmbedding, string) {
	emb := getEmbedding(text)

	// First, let's check if we have any embeddings in the database
	var count int64
	db.Table("taxonomy_embeddings").Count(&count)
	if count == 0 {
		log.Printf("⚠️ No embeddings found in the database")
		return nil, "others"
	}

	// Try vector similarity query
	var result TaxonomyEmbeddingResult
	query := `
		SELECT id, lob, category, subcategory, question, created_at, 
		       (embedding <-> ?::vector) AS distance
		FROM taxonomy_embeddings
		WHERE embedding <-> ?::vector < 1.0
		ORDER BY embedding <-> ?::vector
		LIMIT 1
	`

	embeddingStr := formatVectorForPostgres(emb)
	err := db.Raw(query, embeddingStr, embeddingStr, embeddingStr).Scan(&result).Error
	if err != nil {
		log.Printf("⚠️ Vector similarity query error: %v\n", err)
		return nil, "others"
	}

	if result.ID == 0 {
		log.Printf("⚠️ No taxonomy match found for conversation\n")
		return nil, "others"
	}

	log.Printf("✅ Found match: LOB=%s, Category=%s, Subcategory=%s, Question=%s, Distance=%f\n",
		result.LOB, result.Category, result.Subcategory, result.Question, result.Distance)

	// Convert the result to a TaxonomyEmbedding
	tax := &TaxonomyEmbedding{
		ID:        result.ID,
		LOB:       result.LOB,
		Category:  result.Category,
		Folder:    result.Subcategory,
		Title:     result.Question,
		CreatedAt: result.CreatedAt,
	}

	// Use conversation metadata to help determine insight type
	insightType := determineInsightType(text, topic, status, rating, *tax)
	return tax, insightType
}

func determineInsightType(text, topic, status string, rating int, tax TaxonomyEmbedding) string {
	// First try LLM classification
	llmType := callLLMToClassify(text, tax)
	if llmType != "others" {
		return llmType
	}

	// Fallback to heuristic classification based on conversation metadata
	switch {
	case status == "escalated" || rating <= 2:
		return "complaint"
	case strings.Contains(topic, "feature") || strings.Contains(topic, "suggestion"):
		return "feature-request"
	case rating >= 4:
		return "improvement"
	default:
		return "others"
	}
}

// formatVectorForPostgres converts a float32 array to a string representation
// that PostgreSQL's vector type can understand
func formatVectorForPostgres(embedding pq.Float32Array) string {
	strValues := make([]string, len(embedding))
	for i, v := range embedding {
		strValues[i] = fmt.Sprintf("%f", v)
	}
	return "[" + strings.Join(strValues, ",") + "]"
}

func getEmbedding(text string) pq.Float32Array {
	txt := strings.ReplaceAll(text, "\n", " ")
	data := map[string]interface{}{
		"input": txt,
		"model": embedModel,
	}
	body, _ := json.Marshal(data)

	req, _ := http.NewRequest("POST", embedAPI, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("⚠️ Embed API failed:", err)
		return make(pq.Float32Array, 1536)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	bodyBytes, _ := ioutil.ReadAll(resp.Body)
	_ = json.Unmarshal(bodyBytes, &result)

	if len(result.Data) == 0 {
		return make(pq.Float32Array, 1536)
	}
	return pq.Float32Array(result.Data[0].Embedding)
}

func callLLMToClassify(text string, tax TaxonomyEmbedding) string {
	prompt := fmt.Sprintf(`You are a classification assistant.

Your task is to classify the given user feedback into one of the following insight types:
- complaint: If the user is reporting a problem or expressing frustration.
- improvement: If the user suggests enhancing something that exists.
- feature-request: If the user is asking for a new feature or functionality.
- others: If the feedback doesn't clearly fit into the above.

Context:
Feedback: "%s"
Relevant FAQ Match:
- LOB: %s
- Category: %s
- Subcategory: %s
- Question: %s

Respond with exactly one of the 4 options: complaint, improvement, feature-request, or others. Do not include anything else.`, text, tax.LOB, tax.Category, tax.Folder, tax.Title)

	data := map[string]interface{}{
		"model": chatModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	body, _ := json.Marshal(data)

	req, _ := http.NewRequest("POST", chatAPI, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("❌ LLM request failed:", err)
		return "others"
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if len(result.Choices) == 0 {
		return "others"
	}

	classification := strings.ToLower(strings.TrimSpace(result.Choices[0].Message.Content))
	valid := map[string]bool{"complaint": true, "improvement": true, "feature-request": true, "others": true}
	if valid[classification] {
		return classification
	}
	return "others"
}
