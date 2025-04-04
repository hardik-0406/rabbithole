// store_taxonomy_embeddings.go
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"rabbithole/db"
	"strings"
	"time"

	"github.com/lib/pq"
)

type TaxonomyEmbedding struct {
	ID          uint `gorm:"primaryKey"`
	LOB         string
	Category    string
	Subcategory string
	Question    string
	Embedding   pq.Float32Array `gorm:"type:vector(1536)"`
	CreatedAt   time.Time
}

const (
	embedAPI  = "https://api.rabbithole.cred.club/v1/embeddings"
	apiKey    = "sk-G_BXXmoaRnY5pkImc2yjDw"
	modelName = "text-embedding-3-small"
)

func main() {
	db, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ DB connection error:", err)
	}
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector;").Error; err != nil {
		log.Fatal("❌ Failed to create vector extension:", err)
	}

	// Create the table with the correct vector type
	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS taxonomy_embeddings (
			id SERIAL PRIMARY KEY,
			lob TEXT,
			category TEXT,
			subcategory TEXT,
			question TEXT,
			embedding vector(1536),
			created_at TIMESTAMP
		);
	`).Error; err != nil {
		log.Fatal("❌ Failed to create table:", err)
	}

	file, err := os.Open("/Users/hardik/Downloads/CRED FAQs - FAQ list.csv")
	if err != nil {
		log.Fatal("❌ Failed to read FAQ CSV:", err)
	}
	defer file.Close()

	r := csv.NewReader(file)
	_, _ = r.Read() // skip header

	total := 0
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}

		text := fmt.Sprintf("LOB: %s | Category: %s | Subcategory: %s | Question: %s", rec[0], rec[1], rec[2], rec[3])
		embedding := getEmbedding(text)

		// Use raw SQL to insert the embedding
		query := `
			INSERT INTO taxonomy_embeddings (lob, category, subcategory, question, embedding, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`

		// Convert the embedding to a string representation that PostgreSQL can understand
		embeddingStr := formatVectorForPostgres(embedding)

		if err := db.Exec(query, rec[0], rec[1], rec[2], rec[3], embeddingStr, time.Now()).Error; err != nil {
			log.Printf("⚠️ Failed to insert embedding: %v\n", err)
			continue
		}

		total++
		if total%10 == 0 {
			fmt.Printf("✅ Inserted %d embeddings\n", total)
			time.Sleep(1 * time.Second) // avoid rate limits
		}
	}

	fmt.Printf("🎉 All done. Total entries: %d\n", total)
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
		"model": modelName,
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

	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	_ = json.Unmarshal(respBody, &result)

	if len(result.Data) == 0 {
		return make(pq.Float32Array, 1536)
	}
	return pq.Float32Array(result.Data[0].Embedding)
}
