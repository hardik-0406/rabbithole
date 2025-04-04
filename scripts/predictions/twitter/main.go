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

type Tweet struct {
	ID        uint      `gorm:"primaryKey"`
	TweetID   string    `gorm:"uniqueIndex"`
	Username  string    `gorm:"index"`
	Text      string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
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
	chatModel  = "claude-3-7-sonnet"
)

func main() {
	db, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ DB connection error:", err)
	}

	db.AutoMigrate(&FeedbackInsight{})

	var tweets []Tweet
	db.Find(&tweets)

	for _, tweet := range tweets {
		var exists bool
		db.Raw("SELECT EXISTS (SELECT 1 FROM feedback_insights WHERE original_id = ? AND source = 'twitter')", tweet.ID).Scan(&exists)
		if exists {
			continue
		}

		tax, insightType := categorizeFeedback(tweet.Text, db)
		if tax == nil {
			log.Printf("⚠️ Skipping tweet %s - no taxonomy match\n", tweet.TweetID)
			continue
		}

		insight := FeedbackInsight{
			OriginalID:  tweet.ID,
			Source:      "twitter",
			Username:    tweet.Username,
			Feedback:    tweet.Text,
			LOB:         tax.LOB,
			Category:    tax.Category,
			Subcategory: tax.Folder,
			Question:    tax.Title,
			InsightType: insightType,
			CreatedAt:   time.Now(),
		}
		db.Create(&insight)

		log.Printf("✅ Stored: Tweet %s → %s > %s > %s [%s]",
			tweet.TweetID, tax.Category, tax.Folder, tax.Title, insightType)

		time.Sleep(2 * time.Second)
	}
}

func categorizeFeedback(text string, db *gorm.DB) (*TaxonomyEmbedding, string) {
	emb := getEmbedding(text)

	// Debug: Print the first few values of the embedding
	log.Printf("Debug: Embedding first 5 values: %v", emb[:5])

	// First, let's check if we have any embeddings in the database
	var count int64
	db.Table("taxonomy_embeddings").Count(&count)
	log.Printf("Debug: Found %d embeddings in the database", count)

	if count == 0 {
		log.Printf("⚠️ No embeddings found in the database")
		return nil, "others"
	}

	// Try a simpler query first to see if we can get any results
	var matches []TaxonomyEmbeddingResult
	db.Raw("SELECT id, lob, category, subcategory, question, created_at FROM taxonomy_embeddings LIMIT 5").Scan(&matches)
	log.Printf("Debug: First 5 embeddings in DB: %+v", matches)

	// Now try the vector similarity query with a more lenient threshold
	var result TaxonomyEmbeddingResult

	// Use a more lenient threshold and proper vector syntax
	query := `
		SELECT id, lob, category, subcategory, question, created_at, 
		       (embedding <-> ?::vector) AS distance
		FROM taxonomy_embeddings
		WHERE embedding <-> ?::vector < 1.0
		ORDER BY embedding <-> ?::vector
		LIMIT 1
	`

	// Convert the embedding to a string representation
	embeddingStr := formatVectorForPostgres(emb)
	log.Printf("Debug: Embedding string format: %s", embeddingStr[:100]+"...")

	err := db.Raw(query, embeddingStr, embeddingStr, embeddingStr).Scan(&result).Error
	if err != nil {
		log.Printf("⚠️ Vector similarity query error: %v\n", err)

		// Try a direct query without vector similarity as a fallback
		log.Printf("Trying fallback query...")
		err = db.Raw("SELECT id, lob, category, subcategory, question, created_at FROM taxonomy_embeddings LIMIT 1").Scan(&result).Error
		if err != nil {
			log.Printf("⚠️ Fallback query error: %v\n", err)
			return nil, "others"
		}
		log.Printf("✅ Using fallback match: LOB=%s, Category=%s", result.LOB, result.Category)
	} else if result.ID == 0 {
		log.Printf("⚠️ No taxonomy match found for text: %s\n", text[:50])

		// Try a direct query without vector similarity as a fallback
		log.Printf("Trying fallback query...")
		err = db.Raw("SELECT id, lob, category, subcategory, question, created_at FROM taxonomy_embeddings LIMIT 1").Scan(&result).Error
		if err != nil {
			log.Printf("⚠️ Fallback query error: %v\n", err)
			return nil, "others"
		}
		log.Printf("✅ Using fallback match: LOB=%s, Category=%s", result.LOB, result.Category)
	} else {
		log.Printf("✅ Found match: LOB=%s, Category=%s, Subcategory=%s, Question=%s, Distance=%f\n",
			result.LOB, result.Category, result.Subcategory, result.Question, result.Distance)
	}

	// Convert the result to a TaxonomyEmbedding
	tax := &TaxonomyEmbedding{
		ID:        result.ID,
		LOB:       result.LOB,
		Category:  result.Category,
		Folder:    result.Subcategory,
		Title:     result.Question,
		CreatedAt: result.CreatedAt,
	}

	insightType := callLLMToClassify(text, *tax)
	return tax, insightType
}

// formatVectorForPostgres converts a float32 array to a string representation
// that PostgreSQL's vector type can understand
func formatVectorForPostgres(embedding pq.Float32Array) string {
	// Convert the embedding to a string representation
	// Format: [0.1,0.2,0.3,...]
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
