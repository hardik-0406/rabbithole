package db

import (
	"fmt"

	"rabbithole/models"

	"gorm.io/gorm"
)

// Sources
const (
	SourceAppStore   = "APP_STORE"
	SourcePlayStore  = "PLAY_STORE"
	SourceReddit     = "REDDIT"
	SourceQuora      = "QUORA"
	SourceMouthShut  = "MOUTH_SHUT"
	SourceTrustPilot = "TRUST_PILOT"
	SourceTwitter    = "TWITTER"
	SourceCred       = "CRED"
	SourceOther      = "OTHER"
)

// FeedbackStore handles storing feedback
type FeedbackStore struct {
	db *gorm.DB
}

func NewFeedbackStore(db *gorm.DB) (*FeedbackStore, error) {
	if err := db.AutoMigrate(&models.Feedback{}); err != nil {
		return nil, fmt.Errorf("failed to create feedback table: %w", err)
	}

	return &FeedbackStore{db: db}, nil
}

// StoreFeedback stores a single feedback entry
func (fs *FeedbackStore) StoreFeedback(feedback *models.Feedback) error {
	return fs.db.Create(feedback).Error
}

// StoreFeedbackBatch stores multiple feedback entries in a batch
func (fs *FeedbackStore) StoreFeedbackBatch(feedbacks []*models.Feedback) error {
	return fs.db.Create(feedbacks).Error
}
