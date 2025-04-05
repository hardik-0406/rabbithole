package feed

import (
	"context"
	"rabbithole/models"

	"gorm.io/gorm"
)

// FeedService handles operations related to feedback data
type FeedService struct {
	db *gorm.DB
}

// NewFeedService creates a new instance of FeedService
func NewFeedService(db *gorm.DB) (*FeedService, error) {
	return &FeedService{
		db: db,
	}, nil
}

// FeedResponse represents the response structure for feedback data
type FeedResponse struct {
	ID         uint                `json:"id"`
	Source     string              `json:"source"`
	SourceURL  string              `json:"source_url"`
	AuthorName string              `json:"author_name"`
	Title      string              `json:"title"`
	Content    string              `json:"content"`
	Rating     float32             `json:"rating"`
	PostedAt   string              `json:"posted_at"`
	CreatedAt  string              `json:"created_at"`
	UpdatedAt  string              `json:"updated_at"`
	Prediction *PredictionResponse `json:"prediction,omitempty"`
}

// PredictionResponse represents the prediction data in the response
type PredictionResponse struct {
	FeedbackType string  `json:"feedback_type"`
	LOB          string  `json:"lob"`
	Category     string  `json:"category"`
	Folder       string  `json:"folder"`
	ArticleTitle string  `json:"article_title"`
	Confidence   float32 `json:"confidence"`
}

// GetAllFeedback retrieves all feedback entries with pagination
func (s *FeedService) GetAllFeedback(ctx context.Context, page, pageSize int) ([]FeedResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}

	var total int64
	if err := s.db.Model(&models.Feedback{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Define the query with joins
	query := s.db.Table("feedbacks").
		Select("feedbacks.*, predictions.*").
		Joins("LEFT JOIN predictions ON feedbacks.prediction_id = predictions.id")

	// Get paginated results
	var results []struct {
		models.Feedback
		FeedbackType string  `gorm:"column:feedback_type"`
		LOB          string  `gorm:"column:lob"`
		Category     string  `gorm:"column:category"`
		Folder       string  `gorm:"column:folder"`
		ArticleTitle string  `gorm:"column:article_title"`
		Confidence   float32 `gorm:"column:confidence"`
	}

	if err := query.
		Order("feedbacks.created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&results).Error; err != nil {
		return nil, 0, err
	}

	response := make([]FeedResponse, len(results))
	for i, r := range results {
		response[i] = FeedResponse{
			ID:         r.ID,
			Source:     r.Source,
			SourceURL:  r.SourceURL,
			AuthorName: r.AuthorName,
			Title:      r.Title,
			Content:    r.Content,
			Rating:     r.Rating,
			PostedAt:   r.PostedAt.Format("2006-01-02T15:04:05Z07:00"),
			CreatedAt:  r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt:  r.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}

		// Include prediction data if available
		if r.HasPrediction && r.PredictionID != nil {
			response[i].Prediction = &PredictionResponse{
				FeedbackType: r.FeedbackType,
				LOB:          r.LOB,
				Category:     r.Category,
				Folder:       r.Folder,
				ArticleTitle: r.ArticleTitle,
				Confidence:   r.Confidence,
			}
		}
	}

	return response, total, nil
}
