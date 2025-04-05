package predictions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"rabbithole/models"
	"rabbithole/secrets"
)

const (
	BATCH_SIZE   = 100 // Number of feedbacks to process in each batch
	WORKER_COUNT = 5   // Number of parallel workers
	MAX_RETRIES  = 3   // Maximum retries for failed predictions
	RETRY_DELAY  = 2 * time.Second
)

type Predictor struct {
	db *gorm.DB
}

func NewPredictor(db *gorm.DB) (*Predictor, error) {
	predictor := &Predictor{db: db}

	// Ensure feedback and prediction tables exist
	if err := db.AutoMigrate(&models.Prediction{}, &models.Feedback{}); err != nil {
		return nil, fmt.Errorf("failed to migrate tables: %w", err)
	}

	// Verify taxonomy table
	if err := predictor.verifyTaxonomyTable(); err != nil {
		return nil, fmt.Errorf("taxonomy verification failed: %w", err)
	}

	return predictor, nil
}

// ProcessAllFeedback processes all feedback in batches with parallel workers
func (p *Predictor) ProcessAllFeedback() error {
	var totalCount int64
	// Update the count query to only count feedbacks without predictions
	if err := p.db.Model(&models.Feedback{}).
		Where("prediction_id IS NULL OR has_prediction = ?", false).
		Count(&totalCount).Error; err != nil {
		return fmt.Errorf("failed to get total count: %w", err)
	}

	log.Printf("Starting prediction processing for %d unpredicted feedback entries", totalCount)

	// If no unpredicted feedbacks, return early
	if totalCount == 0 {
		log.Println("No new feedbacks to process")
		return nil
	}

	// Create channels for work distribution
	jobs := make(chan []models.Feedback, WORKER_COUNT)
	results := make(chan error, WORKER_COUNT)
	var wg sync.WaitGroup

	// Start worker pool
	for w := 1; w <= WORKER_COUNT; w++ {
		wg.Add(1)
		go p.worker(w, jobs, results, &wg)
	}

	// Process in batches
	var offset int
	for {
		var feedbacks []models.Feedback
		// Update the query to only fetch feedbacks without predictions
		if err := p.db.Model(&models.Feedback{}).
			Where("prediction_id IS NULL OR has_prediction = ?", false).
			Order("id").
			Limit(BATCH_SIZE).
			Offset(offset).
			Find(&feedbacks).Error; err != nil {
			return fmt.Errorf("failed to fetch batch: %w", err)
		}

		if len(feedbacks) == 0 {
			break
		}

		jobs <- feedbacks
		offset += BATCH_SIZE

		log.Printf("Queued batch of %d unpredicted feedbacks (offset: %d / %d)",
			len(feedbacks), offset, totalCount)
	}

	// Close jobs channel after all batches are queued
	close(jobs)

	// Wait for all workers to complete
	wg.Wait()
	close(results)

	// Check for any errors
	for err := range results {
		if err != nil {
			return fmt.Errorf("worker error: %w", err)
		}
	}

	log.Printf("Successfully processed all %d unpredicted feedback entries", totalCount)
	return nil
}

// worker processes batches of feedback
func (p *Predictor) worker(id int, jobs <-chan []models.Feedback, results chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()

	for feedbacks := range jobs {
		log.Printf("Worker %d processing batch of %d feedbacks", id, len(feedbacks))

		for _, feedback := range feedbacks {
			for retry := 0; retry < MAX_RETRIES; retry++ {
				err := p.ProcessFeedback(&feedback)
				if err == nil {
					break
				}
				if retry == MAX_RETRIES-1 {
					results <- fmt.Errorf("failed to process feedback %d after %d retries: %w", feedback.ID, MAX_RETRIES, err)
					continue
				}
				time.Sleep(RETRY_DELAY)
			}
		}
	}
	results <- nil
}

// ProcessFeedback processes a single feedback entry
func (p *Predictor) ProcessFeedback(feedback *models.Feedback) error {
	// Get embedding for feedback content
	embedding := getEmbedding(feedback.Content)

	// Find matching taxonomy
	taxonomy, err := p.findMatchingTaxonomy(embedding)
	if err != nil {
		return fmt.Errorf("taxonomy matching failed: %w", err)
	}

	// Classify feedback type
	feedbackType := p.classifyFeedbackType(feedback.Content, taxonomy)

	// Create or update prediction
	return p.db.Transaction(func(tx *gorm.DB) error {
		// Delete existing prediction if any
		if feedback.PredictionID != nil {
			if err := tx.Delete(&models.Prediction{}, *feedback.PredictionID).Error; err != nil {
				return fmt.Errorf("failed to delete existing prediction: %w", err)
			}
		}

		// Create new prediction
		prediction := &models.Prediction{
			FeedbackType: feedbackType,
			LOB:          taxonomy.LOB,
			Category:     taxonomy.Category,
			Folder:       taxonomy.Folder,
			ArticleTitle: taxonomy.Question,
			Confidence:   0.8,
			FeedbackID:   feedback.ID,
		}

		if err := tx.Create(prediction).Error; err != nil {
			return fmt.Errorf("failed to create prediction: %w", err)
		}

		// Update feedback with new prediction ID
		if err := tx.Model(feedback).Updates(map[string]interface{}{
			"prediction_id":  prediction.ID,
			"has_prediction": true,
		}).Error; err != nil {
			return fmt.Errorf("failed to update feedback: %w", err)
		}

		return nil
	})
}

func (p *Predictor) findMatchingTaxonomy(embedding pq.Float32Array) (*models.TaxonomyEmbedding, error) {
	var result models.TaxonomyEmbedding

	// First check if we have any taxonomy embeddings - use direct SQL count
	var count int64
	if err := p.db.Raw("SELECT COUNT(*) FROM taxonomy_embeddings").Scan(&count).Error; err != nil {
		return nil, fmt.Errorf("failed to check taxonomy embeddings: %w", err)
	}

	log.Printf("Found %d taxonomy embeddings", count)

	if count == 0 {
		return nil, fmt.Errorf("no taxonomy embeddings found in database")
	}

	// Try to find matching taxonomy with vector similarity
	query := `
		WITH similarity AS (
			SELECT id, lob, category, folder, question, 
				   (embedding <-> ?::vector) as distance
			FROM taxonomy_embeddings
			WHERE embedding IS NOT NULL
			ORDER BY embedding <-> ?::vector
			LIMIT 1
		)
		SELECT * FROM similarity WHERE distance < 0.8;
	`

	embStr := formatVectorForPostgres(embedding)
	err := p.db.Raw(query, embStr, embStr).Scan(&result)

	// Log the query result for debugging
	log.Printf("Taxonomy search result: ID=%d, LOB=%s, Category=%s, Folder=%s, Question=%s",
		result.ID, result.LOB, result.Category, result.Folder, result.Question)

	if err != nil || result.ID == 0 {
		// If no good match found, find the closest match regardless of distance
		fallbackQuery := `
			SELECT id, lob, category, folder, question
			FROM taxonomy_embeddings
			WHERE lob != '' AND category != ''
			ORDER BY embedding <-> ?::vector
			LIMIT 1;
		`

		if err := p.db.Raw(fallbackQuery, embStr).Scan(&result).Error; err != nil {
			log.Printf("Failed to find any taxonomy match: %v", err)
			// Only return default if absolutely nothing is found
			return &models.TaxonomyEmbedding{
				LOB:      "Uncategorized",
				Category: "General",
				Folder:   "Other",
				Question: "Uncategorized Feedback",
			}, nil
		}
	}

	// Validate the result
	if result.LOB == "" || result.Category == "" {
		log.Printf("Warning: Found taxonomy with empty LOB or Category: ID=%d", result.ID)
		// Query a valid taxonomy directly
		var validTaxonomy models.TaxonomyEmbedding
		if err := p.db.Raw("SELECT * FROM taxonomy_embeddings WHERE lob != '' AND category != '' LIMIT 1").Scan(&validTaxonomy).Error; err != nil {
			log.Printf("Failed to find valid taxonomy: %v", err)
			return &models.TaxonomyEmbedding{
				LOB:      "Uncategorized",
				Category: "General",
				Folder:   "Other",
				Question: "Uncategorized Feedback",
			}, nil
		}
		return &validTaxonomy, nil
	}

	return &result, nil
}

func (p *Predictor) classifyFeedbackType(content string, taxonomy *models.TaxonomyEmbedding) string {
	prompt := fmt.Sprintf(`Classify this feedback into one of these types:
- complaint: User reporting a problem or expressing frustration
- improvement: User suggesting enhancement to existing feature
- feature-request: User asking for new functionality
- others: Feedback doesn't fit above categories

Context:
Feedback: "%s"
Related Article:
- LOB: %s
- Category: %s
- Topic: %s
- Title: %s

Respond with exactly one category name only.`,
		content, taxonomy.LOB, taxonomy.Category, taxonomy.Folder, taxonomy.Question)

	response := callLLM(prompt)
	return normalizeFeedbackType(response)
}

// Helper functions from your existing code
func getEmbedding(text string) pq.Float32Array {
	data := map[string]interface{}{
		"input": text,
		"model": secrets.EMBED_MODEL,
	}
	body, _ := json.Marshal(data)

	req, _ := http.NewRequest("POST", secrets.EMBED_API, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secrets.API_KEY)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Embedding API failed: %v", err)
		return make(pq.Float32Array, 1536)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	json.Unmarshal(bodyBytes, &result)

	if len(result.Data) == 0 {
		return make(pq.Float32Array, 1536)
	}
	return pq.Float32Array(result.Data[0].Embedding)
}

func callLLM(prompt string) string {
	data := map[string]interface{}{
		"model": secrets.CHAT_MODEL,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	body, _ := json.Marshal(data)

	req, _ := http.NewRequest("POST", secrets.CHAT_API, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secrets.API_KEY)

	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
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
	return result.Choices[0].Message.Content
}

func normalizeFeedbackType(response string) string {
	response = strings.ToLower(strings.TrimSpace(response))
	validTypes := map[string]bool{
		"complaint":       true,
		"improvement":     true,
		"feature-request": true,
		"others":          true,
	}
	if validTypes[response] {
		return response
	}
	return "others"
}

func formatVectorForPostgres(embedding pq.Float32Array) string {
	strValues := make([]string, len(embedding))
	for i, v := range embedding {
		strValues[i] = fmt.Sprintf("%f", v)
	}
	return "[" + strings.Join(strValues, ",") + "]"
}

func (p *Predictor) verifyTaxonomyTable() error {
	// Check if table exists
	var exists bool
	err := p.db.Raw(`
		SELECT EXISTS (
			SELECT FROM information_schema.tables 
			WHERE table_schema = 'public' 
			AND table_name = 'taxonomy_embeddings'
		);
	`).Scan(&exists).Error

	if err != nil {
		return fmt.Errorf("failed to check taxonomy table existence: %w", err)
	}

	if !exists {
		return fmt.Errorf("taxonomy_embeddings table does not exist")
	}

	// Check table structure
	var columns []struct {
		ColumnName string `gorm:"column:column_name"`
		DataType   string `gorm:"column:data_type"`
	}

	err = p.db.Raw(`
		SELECT column_name, data_type 
		FROM information_schema.columns 
		WHERE table_name = 'taxonomy_embeddings';
	`).Scan(&columns).Error

	if err != nil {
		return fmt.Errorf("failed to check taxonomy table structure: %w", err)
	}

	log.Printf("Taxonomy table structure: %+v", columns)

	// Check data
	var stats struct {
		Total     int64
		WithLOB   int64
		WithEmbed int64
	}

	p.db.Raw("SELECT COUNT(*) FROM taxonomy_embeddings").Scan(&stats.Total)
	p.db.Raw("SELECT COUNT(*) FROM taxonomy_embeddings WHERE lob != ''").Scan(&stats.WithLOB)
	p.db.Raw("SELECT COUNT(*) FROM taxonomy_embeddings WHERE embedding IS NOT NULL").Scan(&stats.WithEmbed)

	log.Printf("Taxonomy statistics: Total=%d, WithLOB=%d, WithEmbeddings=%d",
		stats.Total, stats.WithLOB, stats.WithEmbed)

	return nil
}
