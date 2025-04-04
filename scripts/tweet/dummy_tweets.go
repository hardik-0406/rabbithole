// generate_dummy_tweets.go
package scripts

import (
	"fmt"
	"log"
	"math/rand"
	"rabbithole/db"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

type Tweet struct {
	ID        uint      `gorm:"primaryKey"`
	TweetID   string    `gorm:"uniqueIndex"`
	Username  string    `gorm:"index"`
	Text      string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
}

// More realistic user profiles with engagement patterns
var userProfiles = []struct {
	handle     string
	frequency  int     // tweets per month
	sentiment  float32 // 0 = negative, 1 = positive
	isCustomer bool
}{
	{"@ritesh92", 5, 0.7, true},
	{"@ananyaX", 8, 0.4, true},
	{"@theRealSid", 3, 0.9, true},
	{"@shruti_crd", 10, 0.6, true},
	{"@upiboy", 15, 0.3, true},
	{"@creditqueen", 7, 0.8, true},
	{"@moneywhiz", 6, 0.5, false},
	{"@mumbai_reviewer", 12, 0.4, true},
	{"@lazyinvestor", 4, 0.7, false},
	{"@fintech_baba", 20, 0.6, true},
	{"@tech_savvy_mom", 5, 0.8, true},
	{"@startup_founder", 8, 0.7, true},
	{"@credit_guru", 15, 0.9, false},
	{"@deal_hunter", 10, 0.6, true},
}

// Enhanced tweet categories with more realistic scenarios
var tweetTemplates = map[string][]string{
	"complaint": {
		"@CRED_club payment failed while paying %s bill. Error code: %s. Please help!",
		"Been trying to complete my %s payment on CRED for the last %d minutes. No luck! @CRED_support",
		"@CRED_support my cashback points from last month's %s bill payment haven't been credited yet",
		"App keeps crashing when trying to view my %s rewards. iOS version %s. @CRED_support",
		"@CRED_club why is the UPI payment for %s taking so long? Stuck at processing for %d mins",
	},
	"feature_request": {
		"Hey @CRED_club, would love to see %s integration in the app!",
		"@CRED_support any plans to add %s feature? Would make life much easier",
		"Suggestion for @CRED_club: Please add support for %s. Would be super helpful!",
		"@CRED_club the app would be perfect if you could add %s functionality",
	},
	"positive_feedback": {
		"Just saved ₹%d using CRED rewards! Love how seamless bill payments are @CRED_club 🙌",
		"The new %s feature on @CRED_club is amazing! Using it everyday",
		"Thanks @CRED_support for helping with my %s issue. Resolved in just %d minutes!",
		"Been using CRED for %d months now. Best decision ever! The rewards are incredible @CRED_club",
	},
	"general_engagement": {
		"Anyone else loving the new %s update from @CRED_club?",
		"Quick question @CRED_support - how do I check my %s status?",
		"@CRED_club the %s experience keeps getting better every month!",
		"Just hit %d months of using CRED. The journey has been amazing! @CRED_club",
	},
}

var (
	billTypes       = []string{"electricity", "credit card", "broadband", "mobile", "DTH", "gas"}
	errorCodes      = []string{"UPI001", "PAY404", "TXN201", "NET102"}
	features        = []string{"split bills", "budget tracking", "card recommendations", "expense analytics", "payment reminders"}
	integrations    = []string{"WhatsApp", "Google Calendar", "Apple Wallet", "Splitwise", "Microsoft Money"}
	iosVersions     = []string{"16.5.1", "17.0.1", "17.1.2", "17.2.0"}
	rewardTypes     = []string{"cashback", "travel", "shopping", "dining"}
	processingTimes = []int{5, 10, 15, 20, 30}
	savingAmounts   = []int{500, 1000, 1500, 2000, 2500, 3000}
)

func generateTweet(profile struct {
	handle     string
	frequency  int
	sentiment  float32
	isCustomer bool
}, date time.Time) Tweet {
	var category string
	r := rand.Float32()

	// Select category based on user sentiment and randomness
	if r < 0.1 { // 10% chance for complaints regardless of sentiment
		category = "complaint"
	} else if r < profile.sentiment {
		category = "positive_feedback"
	} else {
		if rand.Float32() < 0.5 {
			category = "feature_request"
		} else {
			category = "general_engagement"
		}
	}

	templates := tweetTemplates[category]
	template := templates[rand.Intn(len(templates))]
	text := template

	// Fill in template variables
	switch category {
	case "complaint":
		text = fmt.Sprintf(template,
			billTypes[rand.Intn(len(billTypes))],
			errorCodes[rand.Intn(len(errorCodes))],
			processingTimes[rand.Intn(len(processingTimes))],
		)
	case "feature_request":
		text = fmt.Sprintf(template,
			features[rand.Intn(len(features))],
		)
	case "positive_feedback":
		text = fmt.Sprintf(template,
			savingAmounts[rand.Intn(len(savingAmounts))],
			rewardTypes[rand.Intn(len(rewardTypes))],
			processingTimes[rand.Intn(len(processingTimes))],
		)
	case "general_engagement":
		text = fmt.Sprintf(template,
			integrations[rand.Intn(len(integrations))],
			rewardTypes[rand.Intn(len(rewardTypes))],
		)
	}

	// Ensure text doesn't exceed Twitter's limit
	if len(text) > 280 {
		text = text[:277] + "..."
	}

	return Tweet{
		TweetID:   fmt.Sprintf("SIM-%d-%s", time.Now().UnixNano(), strings.TrimPrefix(profile.handle, "@")),
		Username:  profile.handle,
		Text:      text,
		CreatedAt: date,
	}
}

func main() {
	log.Println("🚀 Starting dummy tweet generation")

	db, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ Failed to connect to DB:", err)
	}

	if err := db.AutoMigrate(&Tweet{}); err != nil {
		log.Fatal("❌ Failed to migrate DB:", err)
	}

	log.Println("✅ Connected to database and migrated schema")

	// Seed random number generator
	rand.Seed(time.Now().UnixNano())

	// Generate tweets for the last 6 months
	endDate := time.Now()
	startDate := endDate.AddDate(0, -6, 0)
	currentDate := startDate

	var tweets []Tweet
	totalTweets := 0

	log.Printf("📅 Generating tweets from %s to %s", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	for currentDate.Before(endDate) {
		for _, profile := range userProfiles {
			// Determine if user tweets on this day based on their frequency
			if rand.Float32() < (float32(profile.frequency) / 30.0) {
				// Generate 1-3 tweets for this day
				numTweets := rand.Intn(3) + 1
				for i := 0; i < numTweets; i++ {
					// Add some randomness to tweet timing
					tweetTime := currentDate.Add(time.Duration(rand.Intn(24)) * time.Hour)
					tweetTime = tweetTime.Add(time.Duration(rand.Intn(60)) * time.Minute)

					tweet := generateTweet(profile, tweetTime)
					tweets = append(tweets, tweet)
					totalTweets++

					// Batch insert every 100 tweets
					if len(tweets) >= 100 {
						if err := db.Clauses(clause.OnConflict{
							Columns:   []clause.Column{{Name: "tweet_id"}},
							DoNothing: true,
						}).CreateInBatches(tweets, 100).Error; err != nil {
							log.Printf("⚠️  Warning: Failed to insert batch: %v", err)
						}
						log.Printf("💾 Stored batch of %d tweets (Total: %d)", len(tweets), totalTweets)
						tweets = []Tweet{}
					}
				}
			}
		}
		currentDate = currentDate.AddDate(0, 0, 1)
	}

	// Insert any remaining tweets
	if len(tweets) > 0 {
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tweet_id"}},
			DoNothing: true,
		}).CreateInBatches(tweets, 100).Error; err != nil {
			log.Printf("⚠️  Warning: Failed to insert final batch: %v", err)
		}
		log.Printf("💾 Stored final batch of %d tweets", len(tweets))
	}

	log.Printf("✅ Successfully generated %d dummy tweets", totalTweets)
}
