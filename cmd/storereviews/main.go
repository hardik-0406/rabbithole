package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rabbithole/db"
	"rabbithole/models"
)

// ReviewSection represents a parsed markdown section
type ReviewSection struct {
	Title      string
	AuthorName string
	PostedDate string
	Content    string
	Rating     float32
	SourceURL  string
}

func main() {
	log.Println("Starting feedback import process...")

	// Initialize DB
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	store, err := db.NewFeedbackStore(database)
	if err != nil {
		log.Fatalf("Failed to create feedback store: %v", err)
	}

	feedbackDir := "/Users/hardik/hackathon/rabbithole/raw_feedback"
	targetFile := "cred_synthetic_feedback.md"

	// Ensure the path exists
	if _, err := os.Stat(feedbackDir); os.IsNotExist(err) {
		log.Fatalf("Feedback directory does not exist at %s", feedbackDir)
	}

	filePath := filepath.Join(feedbackDir, targetFile)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Fatalf("Target file does not exist at %s", filePath)
	}

	log.Printf("Processing file: %s", targetFile)
	processMarkdownFile(filePath, store)

	log.Println("Feedback import completed!")
}

func processMarkdownFile(filePath string, store *db.FeedbackStore) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("Error reading file %s: %v", filePath, err)
		return
	}

	fileName := filepath.Base(filePath)
	source := determineSource(fileName)
	sections := parseMarkdownSections(string(content))

	for _, section := range sections {
		feedback := &models.Feedback{
			Source:     source,
			SourceURL:  section.SourceURL,
			AuthorName: section.AuthorName,
			Title:      section.Title,
			Content:    section.Content,
			Rating:     section.Rating,
			PostedAt:   parseDate(section.PostedDate),
		}

		if err := store.StoreFeedback(feedback); err != nil {
			log.Printf("Failed to store feedback: %v", err)
		}
	}
}

func processTweetsFile(filePath string, store *db.FeedbackStore) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Error opening tweets file: %v", err)
		return
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var tweets []struct {
		Text      string    `json:"text"`
		Username  string    `json:"username"`
		CreatedAt time.Time `json:"created_at"`
		URL       string    `json:"url"`
	}

	if err := decoder.Decode(&tweets); err != nil {
		log.Printf("Error decoding tweets: %v", err)
		return
	}

	for _, tweet := range tweets {
		feedback := &models.Feedback{
			Source:     db.SourceTwitter, // Add this to db/feedback.go constants
			SourceURL:  tweet.URL,
			AuthorName: tweet.Username,
			Content:    tweet.Text,
			PostedAt:   tweet.CreatedAt,
		}

		if err := store.StoreFeedback(feedback); err != nil {
			log.Printf("Failed to store tweet: %v", err)
		}
	}
}

func parseMarkdownSections(content string) []ReviewSection {
	var sections []ReviewSection
	lines := strings.Split(content, "\n")
	var currentSection *ReviewSection

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "### "):
			// Save previous section if exists
			if currentSection != nil {
				sections = append(sections, *currentSection)
			}
			// Start new section
			currentSection = &ReviewSection{
				Title: strings.TrimPrefix(line, "### "),
			}

		case strings.Contains(line, "**URL**:"):
			if currentSection != nil {
				currentSection.SourceURL = strings.TrimSpace(strings.Split(line, "**URL**:")[1])
			}

		case strings.Contains(line, "*by"):
			if currentSection != nil {
				parts := strings.Split(line, "-")
				if len(parts) >= 2 {
					currentSection.AuthorName = strings.TrimSpace(strings.TrimPrefix(parts[0], "*by"))
					currentSection.PostedDate = strings.TrimSpace(strings.TrimSuffix(parts[1], "*"))
				}
			}

		case strings.Contains(line, "★"):
			if currentSection != nil {
				currentSection.Rating = float32(strings.Count(line, "★"))
			}

		default:
			if currentSection != nil && len(line) > 0 && !strings.HasPrefix(line, "#") {
				if len(currentSection.Content) > 0 {
					currentSection.Content += "\n"
				}
				currentSection.Content += line
			}
		}
	}

	// Add last section
	if currentSection != nil {
		sections = append(sections, *currentSection)
	}

	return sections
}

func determineSource(fileName string) string {
	switch {
	case strings.Contains(fileName, "appstore"):
		return db.SourceAppStore
	case strings.Contains(fileName, "playstore"):
		return db.SourcePlayStore
	case strings.Contains(fileName, "reddit"):
		return db.SourceReddit
	case strings.Contains(fileName, "cred"):
		return db.SourceCred
	case strings.Contains(fileName, "other"):
		return db.SourceOther
	default:
		return "UNKNOWN"
	}
}

func parseDate(dateStr string) time.Time {
	// Try various date formats
	formats := []string{
		"January 2, 2006",
		"Jan 2, 2006",
		"2006-01-02",
		time.RFC3339,
	}

	for _, format := range formats {
		if t, err := time.Parse(format, strings.TrimSpace(dateStr)); err == nil {
			return t
		}
	}

	// Return current time if parsing fails
	return time.Now()
}
