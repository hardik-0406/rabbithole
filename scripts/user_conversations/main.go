// generate_dummy_conversations.go
package main

import (
	"fmt"
	"log"
	"math/rand"
	"rabbithole/db"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type User struct {
	ID        uint   `gorm:"primaryKey"`
	UUID      string `gorm:"uniqueIndex"`
	FullName  string
	Email     string
	Phone     string
	CreatedAt time.Time
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

var agents = []struct {
	id        string
	shift     string // morning, afternoon, night
	expertise []string
}{
	{"agent_rahul", "morning", []string{"payments", "credit_cards", "rewards"}},
	{"agent_priya", "afternoon", []string{"loans", "investments", "credit_score"}},
	{"agent_amit", "night", []string{"technical", "app_issues", "account"}},
	{"agent_neha", "morning", []string{"travel", "rewards", "payments"}},
	{"agent_rajesh", "afternoon", []string{"credit_cards", "loans", "kyc"}},
	{"agent_sneha", "night", []string{"technical", "payments", "rewards"}},
}

var productFeatures = map[string][]string{
	"payments": {
		"credit card bill payment",
		"utility bill payments",
		"rent payment",
		"education fees",
		"municipal taxes",
	},
	"credit_cards": {
		"card application",
		"limit increase",
		"card upgrade",
		"international transactions",
		"card benefits",
	},
	"rewards": {
		"cashback tracking",
		"points redemption",
		"reward store",
		"special offers",
		"partner discounts",
	},
	"loans": {
		"personal loan",
		"buy now pay later",
		"credit line",
		"EMI conversion",
	},
	"travel": {
		"flight booking",
		"hotel reservation",
		"holiday packages",
		"travel insurance",
	},
}

var competitors = map[string]string{
	"Paytm":      "payment processing speed",
	"PhonePe":    "UPI reliability",
	"GooglePay":  "user interface",
	"Slice":      "credit card features",
	"OneCard":    "reward rates",
	"LazyPay":    "buy now pay later",
	"MakeMyTrip": "travel bookings",
}

var commonIssues = map[string][]string{
	"technical": {
		"app crashes during payment",
		"infinite loading screen",
		"OTP not received",
		"biometric login not working",
		"payment confirmation pending",
	},
	"payment_failures": {
		"transaction failed but money deducted",
		"payment stuck in processing",
		"unable to add new card",
		"saved card not working",
		"UPI payment failing repeatedly",
	},
	"reward_issues": {
		"cashback not credited",
		"points expired without notification",
		"unable to redeem rewards",
		"wrong reward amount credited",
		"missing bonus points",
	},
}

// Conversation templates with placeholders
var conversationTemplates = []struct {
	topic     string
	template  string
	sentiment float32 // 0 = negative, 1 = positive
}{
	{
		topic: "payment_failure",
		template: `User: Hi, I've been trying to pay my %s bill but the transaction keeps failing. Error code: %s
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
		sentiment: 0.3,
	},
	{
		topic: "reward_redemption",
		template: `User: Hey, I can't seem to redeem my reward points in the store
Agent: Hi %s, I'll help you with the redemption process. Could you tell me which reward you're trying to redeem?
User: The %s offer showing in my rewards section
Agent: I see. Let me check your points balance and the offer status.
User: I have enough points, already checked
Agent: You're right, you have %d points. There seems to be a temporary glitch. Let me fix this for you.
User: How long will it take?
Agent: Should be done in the next 2-3 minutes. I'm prioritizing this.
User: Ok, waiting...
Agent: It's fixed now! You should be able to redeem the reward. Please try again.
User: Yes, working now. Thanks!`,
		sentiment: 0.7,
	},
	// Add more templates here...
}

func generateRandomPhone() string {
	return "+91" + strconv.Itoa(6000000000+rand.Intn(1000000000))
}

func generateTranscript(user User, topic string) (string, float32) {
	templates := getTopicTemplates(topic)
	template := templates[rand.Intn(len(templates))]
	sentiment := 0.5 + rand.Float32()*0.5 // Base sentiment

	switch topic {
	case "payment_issue":
		return generatePaymentIssueConversation(user, template, sentiment)
	case "reward_issue":
		return generateRewardIssueConversation(user, template, sentiment)
	case "technical_issue":
		return generateTechnicalIssueConversation(user, template, sentiment)
	case "competitor_comparison":
		return generateCompetitorConversation(user, template, sentiment)
	case "feature_request":
		return generateFeatureRequestConversation(user, template, sentiment)
	default:
		return generateGeneralConversation(user, template, sentiment)
	}
}

var paymentIssueTemplates = []string{
	`User: Hi, I'm facing an issue with my %s payment
Agent: Hello %s, I understand you're having trouble with your payment. Could you please provide more details?
User: I've been trying to pay my %s bill of ₹%d but getting error %s
Agent: I apologize for the inconvenience. Let me check this for you.
Agent: I can see the transaction attempts from your account. The issue seems to be with %s
User: This is urgent, I don't want to miss my due date
Agent: I completely understand. Let me help you with an alternative payment method.
User: Will I still get my CRED rewards?
Agent: Absolutely! All your CRED benefits will remain intact.
%s
User: Thanks for helping out
Agent: You're welcome! Is there anything else you need assistance with?
User: No, that's all for now`,

	`User: Payment failed but money deducted!
Agent: Hi %s, I'm sorry to hear that. Don't worry, I'll help you track this payment.
User: It was for my %s bill, amount ₹%d
Agent: Could you share the transaction reference number or time of payment?
User: About 10 minutes ago, got error %s
Agent: Thank you. Let me check the status.
%s
User: How long will this take?
Agent: I've escalated this to our payments team. They'll resolve it within 24-48 hours.
User: That's too long! I need this fixed sooner
Agent: I understand your concern. I'll mark this as high priority.
%s
User: Okay, please keep me updated
Agent: Absolutely! You'll receive updates via SMS and email.`,
}

var rewardIssueTemplates = []string{
	`User: Where are my cashback points from last payment?
Agent: Hello %s, I'll help you track your rewards.
User: I paid my %s bill of ₹%d last week
Agent: Let me check your transaction and rewards history.
%s
User: The app shows different points than what was promised
Agent: I understand. Let me explain how the points are calculated.
%s
User: Okay, but when will I get them?
Agent: Your points will be credited within %d hours. I've marked this for priority processing.
User: Thanks for clarifying
Agent: You're welcome! Feel free to reach out if you need anything else.`,

	`User: Unable to redeem my reward points
Agent: Hi %s, I'll help you with the redemption process.
User: I have %d points but can't use them
Agent: Let me check your rewards account.
%s
User: It's showing some technical error
Agent: I see the issue. There's a temporary glitch in the rewards system.
%s
User: How long will it take to fix?
Agent: Should be resolved in the next %d minutes. I'm monitoring it personally.
User: Alright, I'll try again later
Agent: Perfect! The points will remain valid, don't worry.`,
}

var technicalIssueTemplates = []string{
	`User: App keeps crashing during payment
Agent: Hi %s, I'm sorry you're experiencing this. Let me help.
User: It happens every time I try to pay my %s bill
Agent: Could you tell me your app version and device type?
User: iPhone %s, latest CRED app
Agent: Thank you. Let me check if there are any known issues.
%s
User: It was working fine yesterday
Agent: I understand. We released an update recently that might help.
%s
User: Should I update the app?
Agent: Yes, please update to version %s. It has fixes for this issue.
User: Okay, trying now
Agent: Let me know if you need any help with the update.`,

	`User: Getting network error constantly
Agent: Hello %s, I'll help you resolve the network issue.
User: Can't complete any transaction
Agent: Are you on WiFi or mobile data?
User: Mobile data, %s network
Agent: Let me check our server status.
%s
User: Other apps are working fine
Agent: I understand. We're seeing some connectivity issues with %s network.
%s
User: How long will this take to fix?
Agent: Our team is working on it. Should be resolved in %d minutes.
User: Keep me posted
Agent: Will do! Thank you for your patience.`,
}

var competitorComparisonTemplates = []string{
	`User: Why should I use CRED instead of %s?
Agent: Hello %s! I'd be happy to explain CRED's unique benefits.
User: %s offers better rewards
Agent: Let me share how CRED's rewards program works.
%s
User: But their cashback is instant
Agent: While our cashback might take %d hours, we offer %s
%s
User: Interesting, tell me more
Agent: We also provide exclusive member benefits like %s
User: That's actually good to know
Agent: Would you like me to explain more about these benefits?`,

	`User: %s has lower transaction charges
Agent: Hi %s, let me explain our pricing structure.
User: Why should I pay more?
Agent: CRED actually offers %s
%s
User: But they process payments faster
Agent: Our payments are optimized for security and reliability.
%s
User: What other benefits do you offer?
Agent: We have exclusive partnerships offering %s
%s
User: That's useful information
Agent: Would you like to try these features?`,
}

func generatePaymentIssueConversation(user User, template string, sentiment float32) (string, float32) {
	billTypes := []string{"credit card", "electricity", "broadband", "mobile", "DTH"}
	errorCodes := []string{"PAY001", "NET002", "AUTH003", "PROC004"}
	issues := []string{"bank server", "network connectivity", "security verification"}
	resolutions := []string{
		"Agent: Great news! Your payment has been processed successfully.",
		"Agent: I've applied a special override to process your payment.",
		"Agent: Payment confirmed! You'll receive confirmation in 2 minutes.",
	}

	transcript := fmt.Sprintf(template,
		billTypes[rand.Intn(len(billTypes))],
		user.FullName,
		billTypes[rand.Intn(len(billTypes))],
		1000+rand.Intn(9000),
		errorCodes[rand.Intn(len(errorCodes))],
		issues[rand.Intn(len(issues))],
		resolutions[rand.Intn(len(resolutions))],
	)

	return transcript, sentiment
}

func generateRewardIssueConversation(user User, template string, sentiment float32) (string, float32) {
	points := []int{500, 1000, 2000, 5000}
	timeframes := []int{24, 48, 72}
	explanations := []string{
		"Agent: I can see your payment was processed through a corporate card, which has different reward rates.",
		"Agent: The reward points are calculated based on your card type and spending category.",
		"Agent: There was a delay due to a verification process for high-value transactions.",
	}

	transcript := fmt.Sprintf(template,
		user.FullName,
		points[rand.Intn(len(points))],
		explanations[rand.Intn(len(explanations))],
		timeframes[rand.Intn(len(timeframes))],
	)

	return transcript, sentiment
}

func generateTechnicalIssueConversation(user User, template string, sentiment float32) (string, float32) {
	devices := []string{"14 Pro", "13", "12", "SE"}
	versions := []string{"4.5.0", "4.5.1", "4.6.0"}
	networks := []string{"Airtel", "Jio", "Vodafone"}
	issues := []string{
		"Agent: We're seeing some intermittent issues with the payment gateway.",
		"Agent: There's a known issue with the latest iOS update.",
		"Agent: Our systems are experiencing higher than usual load.",
	}

	transcript := fmt.Sprintf(template,
		user.FullName,
		devices[rand.Intn(len(devices))],
		versions[rand.Intn(len(versions))],
		issues[rand.Intn(len(issues))],
		networks[rand.Intn(len(networks))],
	)

	return transcript, sentiment
}

func generateCompetitorConversation(user User, template string, sentiment float32) (string, float32) {
	competitors := []string{"Paytm", "PhonePe", "Google Pay", "Slice"}
	benefits := []string{
		"exclusive brand partnerships and premium experiences",
		"higher reward rates on premium credit cards",
		"personalized credit card recommendations",
	}
	features := []string{
		"zero convenience fees on credit card payments",
		"exclusive member-only deals",
		"premium customer support",
	}

	transcript := fmt.Sprintf(template,
		competitors[rand.Intn(len(competitors))],
		user.FullName,
		benefits[rand.Intn(len(benefits))],
		features[rand.Intn(len(features))],
	)

	return transcript, sentiment
}

func selectAgent(topic string, conversationTime time.Time) string {
	hour := conversationTime.Hour()
	var availableAgents []string

	// Determine shift
	shift := "night"
	if hour >= 6 && hour < 14 {
		shift = "morning"
	} else if hour >= 14 && hour < 22 {
		shift = "afternoon"
	}

	// Filter agents by shift and expertise
	for _, agent := range agents {
		if agent.shift == shift {
			for _, expertise := range agent.expertise {
				if strings.Contains(topic, expertise) {
					availableAgents = append(availableAgents, agent.id)
					break
				}
			}
		}
	}

	if len(availableAgents) == 0 {
		// Fallback to any agent in the correct shift
		for _, agent := range agents {
			if agent.shift == shift {
				availableAgents = append(availableAgents, agent.id)
			}
		}
	}

	return availableAgents[rand.Intn(len(availableAgents))]
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

func createUsersInBatches(db *gorm.DB, batchSize int) []User {
	var allUsers []User
	log.Printf("🚀 Starting user generation...")

	for i := 0; i < 100000; i++ {
		uuid := fmt.Sprintf("user_%05d", i+1)
		name := fmt.Sprintf("User %05d", i+1)
		email := fmt.Sprintf("%s@example.com", uuid)
		phone := generateRandomPhone()

		// Random date in last 4 years
		createdAt := time.Now().AddDate(-rand.Intn(4), -rand.Intn(12), -rand.Intn(30))

		user := User{
			UUID:      uuid,
			FullName:  name,
			Email:     email,
			Phone:     phone,
			CreatedAt: createdAt,
		}
		allUsers = append(allUsers, user)

		if len(allUsers) >= batchSize {
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(allUsers[len(allUsers)-batchSize:], batchSize).Error; err != nil {
				log.Printf("⚠️ Error creating users batch: %v", err)
			}
			log.Printf("✅ Created batch of %d users (Total: %d)", batchSize, len(allUsers))
		}
	}

	// Insert remaining users
	remainingCount := len(allUsers) % batchSize
	if remainingCount > 0 {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(allUsers[len(allUsers)-remainingCount:], remainingCount).Error; err != nil {
			log.Printf("⚠️ Error creating final users batch: %v", err)
		}
	}

	log.Printf("✅ Completed user generation (Total users: %d)", len(allUsers))
	return allUsers
}

// New function to fetch existing users
func getRandomUsers(db *gorm.DB, limit int) ([]User, error) {
	var users []User
	// Use ORDER BY RANDOM() for PostgreSQL or RAND() for MySQL to get random users
	// Limit the query to avoid loading too many users in memory
	if err := db.Order("RANDOM()").Limit(limit).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch users: %w", err)
	}
	return users, nil
}

func createConversationsOnly(db *gorm.DB) error {
	log.Printf("🎯 Starting conversation generation...")

	// Configuration
	const (
		totalConversations = 500
		batchSize          = 50
		usersPerFetch      = 100 // Number of users to fetch at a time
	)

	var (
		conversations []Conversation
		totalCreated  = 0
		endDate       = time.Now()
		startDate     = endDate.AddDate(-2, 0, 0)
	)

	// Create a buffered channel for user fetching
	userChan := make(chan User, usersPerFetch)
	errorChan := make(chan error, 1)

	// Start a goroutine to feed users
	go func() {
		defer close(userChan)

		for totalCreated < totalConversations {
			users, err := getRandomUsers(db, usersPerFetch)
			if err != nil {
				errorChan <- err
				return
			}

			for _, user := range users {
				userChan <- user
			}
		}
	}()

	// Process conversations in batches
	for totalCreated < totalConversations {
		select {
		case err := <-errorChan:
			return fmt.Errorf("error fetching users: %w", err)
		case user, ok := <-userChan:
			if !ok {
				continue
			}

			// Random date within last 2 years
			conversationDate := startDate.Add(time.Duration(rand.Int63n(int64(endDate.Sub(startDate)))))

			// Select random topic and generate transcript
			topic := getRandomTopic()
			transcript, sentiment := generateTranscript(user, topic)

			// Select appropriate agent based on topic and time
			agent := selectAgent(topic, conversationDate)

			// Determine status and rating based on sentiment
			status := getStatus(sentiment)
			rating := getRating(sentiment)

			conv := Conversation{
				UserUUID:   user.UUID,
				AgentID:    agent,
				Topic:      topic,
				Transcript: transcript,
				Status:     status,
				Rating:     rating,
				CreatedAt:  conversationDate,
			}
			conversations = append(conversations, conv)
			totalCreated++

			// Batch insert when we reach batch size or final records
			if len(conversations) >= batchSize || totalCreated == totalConversations {
				// Use transaction for batch insert
				err := db.Transaction(func(tx *gorm.DB) error {
					if err := tx.CreateInBatches(conversations, len(conversations)).Error; err != nil {
						return fmt.Errorf("failed to create conversations batch: %w", err)
					}
					return nil
				})

				if err != nil {
					log.Printf("⚠️ Error creating conversations batch: %v", err)
					return err
				}

				log.Printf("✅ Created batch of %d conversations (Total: %d/%d)",
					len(conversations), totalCreated, totalConversations)

				// Clear the slice while keeping capacity
				conversations = conversations[:0]
			}
		}
	}

	log.Printf("✅ Completed conversation generation (Total conversations: %d)", totalCreated)
	return nil
}

func getRandomTopic() string {
	topics := []string{
		"payment_issue",
		"reward_issue",
		"technical_issue",
		"competitor_comparison",
		"feature_request",
		"travel_booking",
		"loan_query",
		"kyc_issue",
		"app_performance",
		"security_concern",
	}
	return topics[rand.Intn(len(topics))]
}

func getTopicTemplates(topic string) []string {
	switch topic {
	case "payment_issue":
		return paymentIssueTemplates
	case "reward_issue":
		return rewardIssueTemplates
	case "technical_issue":
		return technicalIssueTemplates
	case "competitor_comparison":
		return competitorComparisonTemplates
	default:
		// Add missing template for feature request
		return []string{
			`User: Hi, I have a suggestion for CRED
Agent: Hello %s, we'd love to hear your feedback!
User: It would be great if you could add %s feature
Agent: Thank you for the suggestion! Let me explain our current roadmap.
%s
User: That makes sense
Agent: Would you like to be notified when we launch new features?
User: Yes, please
Agent: Great! I've added you to our feature updates list.`,
		}
	}
}

func generateFeatureRequestConversation(user User, template string, sentiment float32) (string, float32) {
	features := []string{
		"split bill payments",
		"family card management",
		"expense analytics dashboard",
		"automatic bill reminders",
		"card usage recommendations",
	}
	roadmap := []string{
		"Agent: This feature is actually in our development pipeline for next quarter!",
		"Agent: We're currently working on something similar to this.",
		"Agent: Thanks for the suggestion! I'll share this with our product team.",
	}

	transcript := fmt.Sprintf(template,
		user.FullName,
		features[rand.Intn(len(features))],
		roadmap[rand.Intn(len(roadmap))],
	)

	return transcript, sentiment
}

func generateGeneralConversation(user User, template string, sentiment float32) (string, float32) {
	// Fallback for any unhandled topics
	return fmt.Sprintf(template,
		user.FullName,
		"general inquiry",
		"We appreciate your feedback!",
	), sentiment
}

func main() {
	database, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ Failed to connect to DB:", err)
	}

	// Auto-migrate the schema
	if err := database.AutoMigrate(&User{}, &Conversation{}); err != nil {
		log.Fatal("❌ Failed to migrate schema:", err)
	}

	// Seed random number generator
	rand.Seed(time.Now().UnixNano())

	log.Println("🚀 Starting conversation generation process...")

	// Create only conversations
	if err := createConversationsOnly(database); err != nil {
		log.Fatal("❌ Failed to create conversations:", err)
	}

	log.Println("✅ Data generation completed successfully!")
}
