package models

import (
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// Feedback represents raw user feedback/review from any source
type Feedback struct {
	gorm.Model
	Source     string    `gorm:"index"` // APP_STORE, PLAY_STORE, REDDIT etc
	SourceURL  string    // Original URL/link if available
	AuthorName string    // Name/username of reviewer
	Title      string    // Title/subject if available
	Content    string    `gorm:"type:text"` // The actual feedback/review text
	Rating     float32   // Star rating if available (1-5), can be null
	PostedAt   time.Time // When the feedback was posted

	// Prediction related fields
	HasPrediction bool       `gorm:"index;default:false"` // Flag to track if prediction is done
	PredictionID  *uint      `gorm:"index"`               // Foreign key to Prediction, nullable
	Prediction    Prediction `gorm:"foreignKey:PredictionID"`
}

// Prediction stores the classification and taxonomy mapping for feedback
type Prediction struct {
	gorm.Model
	FeedbackType string  `gorm:"index"` // complaint, improvement, feature-request, others
	LOB          string  `gorm:"index"` // Line of Business
	Category     string  `gorm:"index"` // Product Category
	Folder       string  // Subcategory/Folder
	ArticleTitle string  // Matched FAQ/Article title
	Confidence   float32 // Confidence score of the prediction
	FeedbackID   uint    `gorm:"index"` // Foreign key to Feedback
}

// ProductTaxonomy represents the product taxonomy structure
type ProductTaxonomy struct {
	gorm.Model
	LOB          string `gorm:"index"`
	Category     string `gorm:"index"`
	Folder       string
	ArticleTitle string
}

// TaxonomyEmbedding stores embeddings for taxonomy matching
type TaxonomyEmbedding struct {
	ID        uint `gorm:"primarykey"`
	LOB       string
	Category  string
	Folder    string
	Question  string
	Embedding pq.Float32Array `gorm:"type:vector(1536)"`
}

// InsightGroup represents grouped feedback insights
type InsightGroup struct {
	gorm.Model
	LOB           string  `gorm:"index"`
	Category      string  `gorm:"index"`
	Folder        string  `gorm:"index"`
	FeedbackType  string  `gorm:"index"` // complaint, improvement, feature-request
	Summary       string  `gorm:"type:text"`
	ActionItems   string  `gorm:"type:text"`
	ImpactScore   float32 // Calculated based on feedback count and ratings
	FeedbackCount int
	LastUpdated   time.Time
}

// InsightMetrics represents aggregated metrics for insights
type InsightMetrics struct {
	gorm.Model
	LOB              string `gorm:"index"`
	Category         string `gorm:"index"`
	Folder           string `gorm:"index"`
	FeedbackType     string `gorm:"index"`
	TotalFeedback    int
	AvgRating        float32
	NegativeFeedback int
	PositiveFeedback int
	Trending         bool    // Indicates if this is a trending issue
	ImpactScore      float32 // Normalized impact score
}
