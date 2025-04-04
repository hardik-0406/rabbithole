package rabbithole

import "gorm.io/gorm"

type Tweet struct {
	gorm.Model
	TweetID   string `gorm:"uniqueIndex"`
	Username  string
	Text      string
	CreatedAt string
}
