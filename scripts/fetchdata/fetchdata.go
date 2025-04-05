// scripts/fetchdata/main.go
package fetchdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// Models
type Tweet struct {
	ID        uint      `gorm:"primaryKey"`
	TweetID   string    `gorm:"uniqueIndex"`
	UserUUID  string    `gorm:"index"`
	Text      string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
}

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
	ID          int64  `gorm:"primaryKey"`
	OriginalID  int64  `gorm:"column:original_id"`
	Source      string `gorm:"index"`
	Username    string `gorm:"index"`
	Feedback    string `gorm:"type:text"`
	LOB         string `gorm:"column:lob;index"`
	Category    string `gorm:"index"`
	Subcategory string `gorm:"index"`
	Question    string
	InsightType string    `gorm:"column:insight_type;index"`
	CreatedAt   time.Time `gorm:"index"`
}

// Response types
type DataGenerationResponse struct {
	TweetsGenerated        int    `json:"tweets_generated"`
	ConversationsGenerated int    `json:"conversations_generated"`
	PredictionsProcessed   int    `json:"predictions_processed"`
	Status                 string `json:"status"`
}

// Service
type FeedbackService struct {
	db *gorm.DB
}

// Constants and variables
var (
	userProfiles = []struct {
		handle     string
		frequency  int
		sentiment  float32
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
	}

	agents = []struct {
		id        string
		shift     string
		expertise []string
	}{
		{"agent_rahul", "morning", []string{"payments", "credit_cards", "rewards"}},
		{"agent_priya", "afternoon", []string{"loans", "investments", "credit_score"}},
		{"agent_amit", "night", []string{"technical", "app_issues", "account"}},
		{"agent_neha", "morning", []string{"travel", "rewards", "payments"}},
	}

	topics = []string{
		"payment_issue",
		"reward_issue",
		"technical_issue",
		"feature_request",
		"app_performance",
		"security_concern",
	}

	tweetTemplates = map[string][]string{
		"complaint": {
			"@CRED_club payment failed while paying %s bill. Error code: %s. Please help!",
			"Been trying to complete my %s payment on CRED for the last %d minutes. No luck! @CRED_support",
			"@CRED_support my cashback points from last month's %s bill payment haven't been credited yet",
		},
		"feature_request": {
			"Hey @CRED_club, would love to see %s integration in the app!",
			"@CRED_support any plans to add %s feature? Would make life much easier",
			"Suggestion for @CRED_club: Please add support for %s. Would be super helpful!",
		},
		"positive_feedback": {
			"Just saved ₹%d using CRED rewards! Love how seamless bill payments are @CRED_club 🙌",
			"The new %s feature on @CRED_club is amazing! Using it everyday",
			"Thanks @CRED_support for helping with my %s issue. Resolved in just %d minutes!",
		},
	}

	features = []string{
		"split bills",
		"budget tracking",
		"card recommendations",
		"expense analytics",
		"payment reminders",
	}

	billTypes   = []string{"electricity", "credit card", "broadband", "mobile", "DTH"}
	errorCodes  = []string{"PAY001", "NET002", "AUTH003", "PROC004"}
	rewardTypes = []string{"cashback", "travel", "shopping", "dining"}

	embedAPI   = "https://api.rabbithole.cred.club/v1/embeddings"
	chatAPI    = "https://api.rabbithole.cred.club/v1/chat/completions"
	apiKey     = "sk-G_BXXmoaRnY5pkImc2yjDw"
	embedModel = "text-embedding-3-small"
	chatModel  = "claude-3-7-sonnet"
)

// ConversationTemplate represents a structured conversation template
type ConversationTemplate struct {
	Topic      string
	Text       string
	ParamTypes []string
	MinParams  int
	MaxParams  int
	Sentiment  float32
}

var conversationTemplates = []ConversationTemplate{
	{
		Topic: "payment_failure",
		Text: `User: Hi, I've been trying to pay my %s bill but the transaction keeps failing. Error code: %s
Agent: Hello %s, I understand your concern. Let me help you with the payment issue.
User: It's urgent, I don't want to miss the due date
Agent: I can see the failed transaction. The error seems to be with %s. Let me guide you through an alternative payment method.
User: Will I still get my cashback and rewards?
Agent: Yes, absolutely! All CRED benefits will still apply. Let's complete the payment first.
User: Okay, trying now...
Agent: Perfect! I can confirm your payment of ₹%d has been successful. The reward points will be credited within 24 hours.
User: Finally! Thanks for helping
Agent: You're welcome! Is there anything else you need help with?
User: No, that's all. Thanks again`,
		ParamTypes: []string{"billType", "errorCode", "username", "issue", "amount"},
		MinParams:  5,
		MaxParams:  5,
		Sentiment:  0.3,
	},
	{
		Topic: "feature_request",
		Text: `User: Hi, I have a suggestion for the app
Agent: Hello %s, we'd love to hear your feedback!
User: It would be great if you could add %s feature
Agent: Thank you for the suggestion! Let me explain our current roadmap.
%s
User: That makes sense. When can we expect this?
Agent: While I can't provide an exact timeline, I've noted your feedback and will share it with our product team.
User: Thanks for listening
Agent: You're welcome! Would you like to be notified when we launch new features?
User: Yes, please
Agent: Great! I've added you to our feature updates list.`,
		ParamTypes: []string{"username", "feature", "roadmapResponse"},
		MinParams:  3,
		MaxParams:  3,
		Sentiment:  0.7,
	},
}

// Template represents a feedback template with its required parameters
type Template struct {
	Text       string
	ParamTypes []string // Types of parameters expected: "amount", "feature", "billType", "errorCode", "minutes", etc.
	MinParams  int      // Minimum number of parameters required
	MaxParams  int      // Maximum number of parameters allowed
}

// TemplateData contains the actual data for template parameters
type TemplateData struct {
	Amount    int
	Feature   string
	BillType  string
	ErrorCode string
	Minutes   int
	// ... other parameter types
}

// Define complete template categories with validation
var feedbackTemplates = map[string][]Template{
	"complaint": {
		{
			Text:       "@CRED_club payment failed while paying %s bill. Error code: %s. Please help!",
			ParamTypes: []string{"billType", "errorCode"},
			MinParams:  2,
			MaxParams:  2,
		},
		{
			Text:       "Been trying to complete my %s payment on CRED for the last %d minutes. No luck! @CRED_support",
			ParamTypes: []string{"billType", "minutes"},
			MinParams:  2,
			MaxParams:  2,
		},
		{
			Text:       "@CRED_support my cashback points from last month's %s bill payment haven't been credited yet",
			ParamTypes: []string{"billType"},
			MinParams:  1,
			MaxParams:  1,
		},
	},
	"feature_request": {
		{
			Text:       "Hey @CRED_club, would love to see %s integration in the app!",
			ParamTypes: []string{"feature"},
			MinParams:  1,
			MaxParams:  1,
		},
		{
			Text:       "@CRED_support any plans to add %s feature? Would make life much easier",
			ParamTypes: []string{"feature"},
			MinParams:  1,
			MaxParams:  1,
		},
		{
			Text:       "Suggestion for @CRED_club: Please add support for %s. Would be super helpful!",
			ParamTypes: []string{"feature"},
			MinParams:  1,
			MaxParams:  1,
		},
	},
	"improvement": {
		{
			Text:       "Just saved ₹%d using CRED rewards! Love how seamless bill payments are @CRED_club 🙌",
			ParamTypes: []string{"amount"},
			MinParams:  1,
			MaxParams:  1,
		},
		{
			Text:       "The new %s feature on @CRED_club is amazing! Using it everyday",
			ParamTypes: []string{"feature"},
			MinParams:  1,
			MaxParams:  1,
		},
		{
			Text:       "Thanks @CRED_support for helping with my %s issue. Resolved in just %d minutes!",
			ParamTypes: []string{"billType", "minutes"},
			MinParams:  2,
			MaxParams:  2,
		},
	},
}

// TemplateProcessor handles safe template generation
type TemplateProcessor struct {
	templates map[string][]Template
	data      map[string][]string
}

func NewTemplateProcessor() *TemplateProcessor {
	return &TemplateProcessor{
		templates: feedbackTemplates,
		data: map[string][]string{
			"amount": {"1000", "2000", "3000", "4000", "5000", "7500", "10000"},
			"feature": {
				"split bills with friends",
				"budget analytics dashboard",
				"card usage insights",
				"automatic bill categorization",
				"payment reminders",
				"reward points calculator",
				"family card management",
			},
			"billType": {
				"electricity",
				"credit card",
				"broadband",
				"mobile",
				"DTH",
				"water",
				"gas",
			},
			"errorCode": {
				"PAY001", "NET002", "AUTH003", "PROC004",
				"TXN101", "SEC202", "VAL303", "SYS404",
			},
			"minutes": {"5", "10", "15", "20", "30", "45", "60"},
			"issue": {
				"payment gateway timeout",
				"network connectivity",
				"server response",
				"authentication failure",
				"bank server issue",
			},
			"roadmapResponse": {
				"Agent: This feature is actually in our development pipeline for next quarter!",
				"Agent: We're currently working on something similar to this.",
				"Agent: Thanks for the suggestion! I'll share this with our product team.",
				"Agent: This is a popular request and we're actively considering it.",
			},
		},
	}
}

func (tp *TemplateProcessor) GenerateFeedback(category string) (string, error) {
	templates, ok := tp.templates[category]
	if !ok {
		return "", fmt.Errorf("invalid category: %s", category)
	}

	// Select random template
	template := templates[rand.Intn(len(templates))]

	// Generate parameters
	var params []interface{}
	for _, paramType := range template.ParamTypes {
		data, ok := tp.data[paramType]
		if !ok {
			return "", fmt.Errorf("missing data for parameter type: %s", paramType)
		}

		switch paramType {
		case "amount":
			amount, _ := strconv.Atoi(data[rand.Intn(len(data))])
			params = append(params, amount)
		case "minutes":
			minutes, _ := strconv.Atoi(data[rand.Intn(len(data))])
			params = append(params, minutes)
		default:
			params = append(params, data[rand.Intn(len(data))])
		}
	}

	// Validate parameter count
	if len(params) < template.MinParams || len(params) > template.MaxParams {
		return "", fmt.Errorf("invalid parameter count: got %d, expected between %d and %d",
			len(params), template.MinParams, template.MaxParams)
	}

	// Generate text with proper error handling
	text := fmt.Sprintf(template.Text, params...)

	// Validate generated text
	if strings.Contains(text, "%!") || strings.Contains(text, "(MISSING)") {
		return "", fmt.Errorf("template formatting error in generated text: %s", text)
	}

	return text, nil
}

func (tp *TemplateProcessor) GenerateConversation(template ConversationTemplate) (string, error) {
	var params []interface{}

	// Generate parameters based on types
	for _, paramType := range template.ParamTypes {
		data, ok := tp.data[paramType]
		if !ok {
			return "", fmt.Errorf("missing data for parameter type: %s", paramType)
		}

		switch paramType {
		case "amount":
			amount, _ := strconv.Atoi(data[rand.Intn(len(data))])
			params = append(params, amount)
		case "username":
			params = append(params, data[rand.Intn(len(data))])
		case "billType":
			params = append(params, data[rand.Intn(len(data))])
		case "errorCode":
			params = append(params, data[rand.Intn(len(data))])
		case "issue":
			params = append(params, data[rand.Intn(len(data))])
		case "feature":
			params = append(params, data[rand.Intn(len(data))])
		case "roadmapResponse":
			params = append(params, data[rand.Intn(len(data))])
		}
	}

	// Validate parameter count
	if len(params) < template.MinParams || len(params) > template.MaxParams {
		return "", fmt.Errorf("invalid parameter count: got %d, expected between %d and %d",
			len(params), template.MinParams, template.MaxParams)
	}

	// Generate conversation with proper error handling
	text := fmt.Sprintf(template.Text, params...)

	// Validate generated text
	if strings.Contains(text, "%!") || strings.Contains(text, "(MISSING)") {
		return "", fmt.Errorf("template formatting error in generated text: %s", text)
	}

	return text, nil
}

// NewFeedbackService creates a new instance of FeedbackService
func NewFeedbackService(db *gorm.DB) (*FeedbackService, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	// Auto-migrate the schema
	if err := db.AutoMigrate(&Tweet{}, &Conversation{}, &FeedbackInsight{}, &TaxonomyEmbedding{}); err != nil {
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}

	return &FeedbackService{
		db: db,
	}, nil
}

// GenerateData generates sample data and processes it
func (s *FeedbackService) GenerateData(ctx context.Context) (*DataGenerationResponse, error) {
	log.Println("🚀 Starting data generation process")

	// Create channels for parallel processing results
	type result struct {
		count int
		err   error
	}

	tweetChan := make(chan result, 1)
	convChan := make(chan result, 1)

	// Create a WaitGroup for coordinating goroutines
	var wg sync.WaitGroup
	wg.Add(2) // For tweets and conversations generation

	// Generate tweets in parallel
	go func() {
		defer wg.Done()
		log.Println("📝 Generating tweets...")
		tweets, err := s.generateTweets(100)
		tweetChan <- result{count: len(tweets), err: err}
	}()

	// Generate conversations in parallel
	go func() {
		defer wg.Done()
		log.Println("💬 Generating conversations...")
		convs, err := s.generateConversations(50)
		convChan <- result{count: len(convs), err: err}
	}()

	// Wait for data generation to complete
	go func() {
		wg.Wait()
		close(tweetChan)
		close(convChan)
	}()

	// Process results with timeout
	timeout := time.After(5 * time.Minute)
	var tweetResult, convResult result

	// Wait for both generations or timeout
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("operation cancelled: %w", ctx.Err())
		case <-timeout:
			return nil, fmt.Errorf("operation timed out after 5 minutes")
		case tr, ok := <-tweetChan:
			if ok && tr.err != nil {
				return nil, fmt.Errorf("failed to generate tweets: %w", tr.err)
			}
			tweetResult = tr
		case cr, ok := <-convChan:
			if ok && cr.err != nil {
				return nil, fmt.Errorf("failed to generate conversations: %w", cr.err)
			}
			convResult = cr
		}
	}

	log.Printf("✅ Generated %d tweets and %d conversations", tweetResult.count, convResult.count)

	// Run predictions
	log.Println("🔄 Processing predictions...")
	predCount, err := s.processPredictions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to process predictions: %w", err)
	}

	log.Printf("✅ Processed %d predictions", predCount)

	return &DataGenerationResponse{
		TweetsGenerated:        tweetResult.count,
		ConversationsGenerated: convResult.count,
		PredictionsProcessed:   predCount,
		Status:                 "success",
	}, nil
}

func (s *FeedbackService) generateTweets(count int) ([]Tweet, error) {
	var tweets []Tweet
	var errors []error
	endDate := time.Now()
	startDate := endDate.AddDate(0, -1, 0)

	// Fetch random users from DB
	var users []User
	if err := s.db.Order("RANDOM()").Limit(100).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch users: %w", err)
	}

	// Map users to personas randomly
	userToPersona := make(map[string]UserPersona)
	for _, user := range users {
		persona := userPersonas[rand.Intn(len(userPersonas))]
		userToPersona[user.UUID] = persona
	}

	for _, user := range users {
		persona := userToPersona[user.UUID]
		numTweets := rand.Intn(count/len(users)) + 1

		for i := 0; i < numTweets; i++ {
			tweetTime := startDate.Add(time.Duration(rand.Int63n(int64(endDate.Sub(startDate)))))
			tweet, err := s.generateTweet(user.UUID, persona, tweetTime)
			if err != nil {
				errors = append(errors, fmt.Errorf("failed to generate tweet for user %s: %w", user.UUID, err))
				continue
			}
			tweets = append(tweets, tweet)
		}
	}

	// Report any errors that occurred during generation
	if len(errors) > 0 {
		return tweets, fmt.Errorf("encountered %d errors during tweet generation: %v", len(errors), errors)
	}

	// Store tweets in batches with validation
	if err := s.db.CreateInBatches(tweets, 100).Error; err != nil {
		return nil, fmt.Errorf("failed to store tweets: %w", err)
	}

	return tweets, nil
}

func (s *FeedbackService) generateTweet(userUUID string, persona UserPersona, tweetTime time.Time) (Tweet, error) {
	tp := NewTemplateProcessor()

	// Determine category based on sentiment
	category := determineFeedbackCategory(persona.Sentiment, persona)

	// Generate feedback text
	text, err := tp.GenerateFeedback(category)
	if err != nil {
		return Tweet{}, fmt.Errorf("failed to generate tweet text: %w", err)
	}

	return Tweet{
		TweetID:   fmt.Sprintf("SIM-%d-%s", time.Now().UnixNano(), userUUID),
		UserUUID:  userUUID,
		Text:      text,
		CreatedAt: tweetTime,
	}, nil
}

func determineFeedbackCategory(sentiment float32, persona UserPersona) string {
	r := rand.Float32()

	// Higher chance of complaints for low sentiment users
	if r < 0.2 || sentiment < 0.3 {
		return "complaint"
	}

	// More feature requests from frequent users
	if persona.Frequency > 7 && r < 0.6 {
		return "feature_request"
	}

	// More improvements from satisfied customers
	if persona.Sentiment > 0.7 && r < 0.8 {
		return "improvement"
	}

	// Fallback distribution
	if r < 0.4 {
		return "complaint"
	} else if r < 0.7 {
		return "feature_request"
	}
	return "improvement"
}

func (s *FeedbackService) generateConversations(count int) ([]Conversation, error) {
	var conversations []Conversation
	endDate := time.Now()
	startDate := endDate.AddDate(0, -1, 0)

	// Fetch random users
	var users []User
	if err := s.db.Order("RANDOM()").Limit(count).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch users: %w", err)
	}

	// Map users to personas
	userToPersona := make(map[string]UserPersona)
	for _, user := range users {
		persona := userPersonas[rand.Intn(len(userPersonas))]
		userToPersona[user.UUID] = persona
	}

	for _, user := range users {
		convTime := startDate.Add(time.Duration(rand.Int63n(int64(endDate.Sub(startDate)))))
		topic := topics[rand.Intn(len(topics))]
		agent := agents[rand.Intn(len(agents))]
		persona := userToPersona[user.UUID]

		// Select template based on persona and topic
		template := selectTemplate(persona, topic)
		transcript, err := s.generateTranscript(template, user, topic)
		if err != nil {
			log.Printf("Error generating transcript for user %s: %v", user.UUID, err)
			continue
		}

		conv := Conversation{
			UserUUID:   user.UUID,
			AgentID:    agent.id,
			Topic:      topic,
			Transcript: transcript,
			Status:     getStatus(template.Sentiment),
			Rating:     getRating(template.Sentiment),
			CreatedAt:  convTime,
		}

		conversations = append(conversations, conv)
	}

	// Store conversations in batches
	if err := s.db.CreateInBatches(conversations, 50).Error; err != nil {
		return nil, fmt.Errorf("failed to store conversations: %w", err)
	}

	return conversations, nil
}

// Helper function to select appropriate template
func selectTemplate(persona UserPersona, topic string) ConversationTemplate {
	// Default to first template if no match
	if len(conversationTemplates) == 0 {
		return ConversationTemplate{}
	}

	// Try to find a template matching the topic
	for _, t := range conversationTemplates {
		if t.Topic == topic {
			return t
		}
	}

	// Fallback to random template
	return conversationTemplates[rand.Intn(len(conversationTemplates))]
}

// Helper function to generate transcript
func (s *FeedbackService) generateTranscript(template ConversationTemplate, user User, topic string) (string, error) {
	tp := NewTemplateProcessor()
	return tp.GenerateConversation(template)
}

func getStatus(sentiment float32) string {
	if sentiment < 0.3 {
		return "escalated"
	} else if sentiment < 0.7 {
		return "resolved"
	}
	return "resolved"
}

func getRating(sentiment float32) int {
	if sentiment < 0.2 {
		return 1
	} else if sentiment < 0.4 {
		return 2
	} else if sentiment < 0.6 {
		return 3
	} else if sentiment < 0.8 {
		return 4
	}
	return 5
}

func (s *FeedbackService) processPredictions(ctx context.Context) (int, error) {
	var (
		totalPredictions int
		wg               sync.WaitGroup
		mu               sync.Mutex
		errChan          = make(chan error, 2)
	)

	wg.Add(2)

	// Process tweets
	go func() {
		defer wg.Done()
		count, err := s.processTweetPredictions()
		if err != nil {
			errChan <- fmt.Errorf("tweet predictions failed: %w", err)
			return
		}
		mu.Lock()
		totalPredictions += count
		mu.Unlock()
	}()

	// Process conversations
	go func() {
		defer wg.Done()
		count, err := s.processConversationPredictions()
		if err != nil {
			errChan <- fmt.Errorf("conversation predictions failed: %w", err)
			return
		}
		mu.Lock()
		totalPredictions += count
		mu.Unlock()
	}()

	// Wait for completion or context cancellation
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
		close(errChan)
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case err := <-errChan:
		if err != nil {
			return 0, err
		}
	case <-done:
		return totalPredictions, nil
	}

	return totalPredictions, nil
}

func (s *FeedbackService) processTweetPredictions() (int, error) {
	var tweets []Tweet
	if err := s.db.Find(&tweets).Error; err != nil {
		return 0, fmt.Errorf("failed to fetch tweets: %w", err)
	}

	var insights []FeedbackInsight
	for _, tweet := range tweets {
		// Check if insight already exists
		var exists bool
		s.db.Raw("SELECT EXISTS (SELECT 1 FROM feedback_insights WHERE original_id = ? AND source = 'twitter')", tweet.ID).Scan(&exists)
		if exists {
			continue
		}

		tax, insightType := s.categorizeTweet(tweet)
		if tax == nil {
			log.Printf("⚠️ Skipping tweet %s - no taxonomy match\n", tweet.TweetID)
			continue
		}

		insight := FeedbackInsight{
			OriginalID:  int64(tweet.ID),
			Source:      "twitter",
			Username:    tweet.UserUUID,
			Feedback:    tweet.Text,
			LOB:         tax.LOB,
			Category:    tax.Category,
			Subcategory: tax.Folder,
			Question:    tax.Title,
			InsightType: insightType,
			CreatedAt:   time.Now(),
		}
		insights = append(insights, insight)

		log.Printf("✅ Processed: Tweet %s → %s > %s > %s [%s]",
			tweet.TweetID, tax.Category, tax.Folder, tax.Title, insightType)

		time.Sleep(2 * time.Second) // Rate limiting
	}

	if len(insights) > 0 {
		if err := s.db.CreateInBatches(insights, 100).Error; err != nil {
			return 0, fmt.Errorf("failed to store tweet insights: %w", err)
		}
	}

	return len(insights), nil
}

func (s *FeedbackService) processConversationPredictions() (int, error) {
	var conversations []Conversation
	if err := s.db.Find(&conversations).Error; err != nil {
		return 0, fmt.Errorf("failed to fetch conversations: %w", err)
	}

	var insights []FeedbackInsight
	for _, conv := range conversations {
		// Check if insight already exists
		var exists bool
		s.db.Raw("SELECT EXISTS (SELECT 1 FROM feedback_insights WHERE original_id = ? AND source = 'conversation')", conv.ID).Scan(&exists)
		if exists {
			continue
		}

		// Extract user messages from transcript
		userMessages := extractUserMessages(conv.Transcript)
		if len(userMessages) == 0 {
			log.Printf("⚠️ Skipping conversation %d - no user messages found\n", conv.ID)
			continue
		}

		// Combine user messages for analysis
		combinedText := strings.Join(userMessages, " ")

		tax, insightType := s.categorizeConversation(combinedText, conv)
		if tax == nil {
			log.Printf("⚠️ Skipping conversation %d - no taxonomy match\n", conv.ID)
			continue
		}

		insight := FeedbackInsight{
			OriginalID:  int64(conv.ID),
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
		insights = append(insights, insight)

		log.Printf("✅ Processed: Conversation %d → %s > %s > %s [%s]",
			conv.ID, tax.Category, tax.Folder, tax.Title, insightType)

		time.Sleep(2 * time.Second) // Rate limiting
	}

	if len(insights) > 0 {
		if err := s.db.CreateInBatches(insights, 100).Error; err != nil {
			return 0, fmt.Errorf("failed to store conversation insights: %w", err)
		}
	}

	return len(insights), nil
}

// Add helper functions
func extractUserMessages(transcript string) []string {
	var userMessages []string
	lines := strings.Split(transcript, "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "User:") {
			message := strings.TrimSpace(strings.TrimPrefix(line, "User:"))
			if message != "" {
				userMessages = append(userMessages, message)
			}
		}
	}

	return userMessages
}

func (s *FeedbackService) categorizeTweet(tweet Tweet) (*TaxonomyEmbedding, string) {
	emb := getEmbedding(tweet.Text)

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
	err := s.db.Raw(query, embeddingStr, embeddingStr, embeddingStr).Scan(&result).Error
	if err != nil {
		log.Printf("⚠️ Vector similarity query error: %v\n", err)
		return nil, "others"
	}

	if result.ID == 0 {
		log.Printf("⚠️ No taxonomy match found for tweet\n")
		return nil, "others"
	}

	tax := &TaxonomyEmbedding{
		ID:        result.ID,
		LOB:       result.LOB,
		Category:  result.Category,
		Folder:    result.Subcategory,
		Title:     result.Question,
		CreatedAt: result.CreatedAt,
	}

	insightType := callLLMToClassify(tweet.Text, *tax)
	return tax, insightType
}

func (s *FeedbackService) categorizeConversation(text string, conv Conversation) (*TaxonomyEmbedding, string) {
	emb := getEmbedding(text)

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
	err := s.db.Raw(query, embeddingStr, embeddingStr, embeddingStr).Scan(&result).Error
	if err != nil {
		log.Printf("⚠️ Vector similarity query error: %v\n", err)
		return nil, "others"
	}

	if result.ID == 0 {
		log.Printf("⚠️ No taxonomy match found for conversation\n")
		return nil, "others"
	}

	tax := &TaxonomyEmbedding{
		ID:        result.ID,
		LOB:       result.LOB,
		Category:  result.Category,
		Folder:    result.Subcategory,
		Title:     result.Question,
		CreatedAt: result.CreatedAt,
	}

	insightType := determineInsightType(text, conv.Topic, conv.Status, conv.Rating, *tax)
	return tax, insightType
}

func determineInsightType(text, topic, status string, rating int, tax TaxonomyEmbedding) string {
	// First try LLM classification
	llmType := callLLMToClassify(text, tax)
	if llmType != "others" {
		return llmType
	}

	// Fallback to heuristic classification
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

type UserPersona struct {
	Type       string  // e.g., "tech_savvy", "finance_focused", etc.
	Frequency  int     // 1-10 scale of how often they post
	Sentiment  float32 // 0-1 scale
	IsCustomer bool
}

var userPersonas = []UserPersona{
	{Type: "tech_savvy", Frequency: 8, Sentiment: 0.7, IsCustomer: true},
	{Type: "finance_focused", Frequency: 6, Sentiment: 0.8, IsCustomer: true},
	{Type: "rewards_hunter", Frequency: 9, Sentiment: 0.6, IsCustomer: true},
	{Type: "casual_user", Frequency: 4, Sentiment: 0.5, IsCustomer: true},
	{Type: "power_user", Frequency: 7, Sentiment: 0.7, IsCustomer: true},
	{Type: "new_user", Frequency: 3, Sentiment: 0.3, IsCustomer: true},
	{Type: "investor", Frequency: 5, Sentiment: 0.8, IsCustomer: true},
	{Type: "shopper", Frequency: 8, Sentiment: 0.6, IsCustomer: true},
	{Type: "security_conscious", Frequency: 4, Sentiment: 0.5, IsCustomer: true},
	{Type: "business_user", Frequency: 7, Sentiment: 0.7, IsCustomer: true},
}

// Add User struct since we're using it
type User struct {
	ID        uint   `gorm:"primaryKey"`
	UUID      string `gorm:"uniqueIndex"`
	FullName  string
	Email     string
	Phone     string
	CreatedAt time.Time
}
