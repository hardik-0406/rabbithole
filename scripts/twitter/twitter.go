// main.go
package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"rabbithole"
	"rabbithole/db"
	"strconv"
	"sync"
	"syscall"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Add a config struct
type TwitterConfig struct {
	APIKey       string
	APIKeySecret string
	BearerToken  string
	AccessToken  string
}

// Add more sophisticated rate limit tracking
var (
	lastRequestTime     time.Time
	minRequestGap       = 5 * time.Second // Increased from 3s to 5s
	requestCount        = 0
	maxRequestsPer15Min = 450 // Twitter's standard rate limit
	windowStart         = time.Now()
	mu                  sync.Mutex // Mutex for thread-safe rate limit tracking
)

// Add a rate limit checker function
func checkRateLimit() time.Duration {
	mu.Lock()
	defer mu.Unlock()

	now := time.Now()

	// Reset window if 15 minutes have passed
	if now.Sub(windowStart) > 15*time.Minute {
		windowStart = now
		requestCount = 0
	}

	// Check if we're approaching rate limit
	if requestCount >= maxRequestsPer15Min {
		// Wait until the current window expires
		return time.Until(windowStart.Add(15 * time.Minute))
	}

	// Check minimum gap between requests
	timeSinceLastRequest := time.Since(lastRequestTime)
	if timeSinceLastRequest < minRequestGap {
		return minRequestGap - timeSinceLastRequest
	}

	return 0
}

// Tweet model for GORM
type Tweet struct {
	ID        uint      `gorm:"primaryKey"`
	TweetID   string    `gorm:"uniqueIndex"`
	Username  string    `gorm:"index"`
	Text      string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
}

// Add TableName method to specify custom table name
func (Tweet) TableName() string {
	return "tweets_v2"
}

// TwitterResponse structure for API response
type TwitterResponse struct {
	Data []struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		AuthorID  string `json:"author_id"`
		CreatedAt string `json:"created_at"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"users"`
	} `json:"includes"`
	Meta struct {
		NextToken string `json:"next_token"`
	} `json:"meta"`
}

// FetchTweetsForHandle fetches tweets for a given Twitter handle
func FetchTweetsForHandle(db *gorm.DB, handle string, start, end time.Time) error {
	log.Printf("🚀 Starting tweet fetch for @%s", handle)
	log.Printf("📅 Time range: %s to %s", start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"))

	maxPages := 5 // Reduced from 10 to 5 to be more conservative
	nextToken := ""
	totalTweets := 0
	retryCount := 0
	maxRetries := 3
	pageDelay := 10 * time.Second // Increased from 5s to 10s

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	log.Printf("⚙️  HTTP client configured with 10s timeout")

	for i := 0; i < maxPages; i++ {
		log.Printf("📃 Fetching page %d for @%s...", i+1, handle)

		tweets, token, err := fetchTweetPage(client, handle, start, end, nextToken)
		if err != nil {
			if retryCount < maxRetries {
				retryCount++
				log.Printf("🔄 Retry %d/%d after error: %v", retryCount, maxRetries, err)
				time.Sleep(time.Duration(retryCount*5) * time.Second)
				continue
			}
			return fmt.Errorf("page %d fetch failed after %d retries: %w", i+1, maxRetries, err)
		}
		retryCount = 0 // Reset retry count on success

		if err := storeTweets(db, tweets); err != nil {
			return fmt.Errorf("storage failed for page %d: %w", i+1, err)
		}

		totalTweets += len(tweets)
		log.Printf("💾 Page %d: Stored %d tweets (Total: %d)", i+1, len(tweets), totalTweets)

		if token == "" {
			log.Printf("🏁 No more pages available for @%s", handle)
			break
		}
		nextToken = token

		// Add longer delay between pages
		log.Printf("😴 Waiting between pages...")
		time.Sleep(pageDelay)
	}

	log.Printf("✅ Completed fetching tweets for @%s. Total tweets: %d", handle, totalTweets)
	return nil
}

func fetchTweetPage(client *http.Client, handle string, start, end time.Time, nextToken string) ([]Tweet, string, error) {
	// Check rate limits
	if waitTime := checkRateLimit(); waitTime > 0 {
		log.Printf("⏳ Rate limiting: Waiting %v before next request...", waitTime.Round(time.Second))
		time.Sleep(waitTime)
	}

	mu.Lock()
	requestCount++
	lastRequestTime = time.Now()
	mu.Unlock()

	log.Printf("🔍 Preparing API request for @%s", handle)

	// Ensure end time is at least 10 seconds in the past
	endTime := time.Now().Add(-10 * time.Second)
	if end.After(endTime) {
		end = endTime
		log.Printf("⚠️  Adjusted end time to be 10 seconds in the past: %s", end.Format(time.RFC3339))
	}

	query := fmt.Sprintf("from:%s lang:en -is:retweet", handle)
	params := url.Values{
		"query":        {query},
		"tweet.fields": {"created_at,author_id"},
		"expansions":   {"author_id"},
		"user.fields":  {"username"},
		"max_results":  {"10"}, // Reduced from 100 to avoid rate limits
	}

	// Recent search API doesn't need start_time that's too old
	sevenDaysAgo := time.Now().AddDate(0, 0, -7)
	if start.After(sevenDaysAgo) {
		params.Add("start_time", start.Format(time.RFC3339))
	} else {
		params.Add("start_time", sevenDaysAgo.Format(time.RFC3339))
		log.Printf("⚠️  Note: Can only fetch tweets from the last 7 days. Adjusted start date to: %s", sevenDaysAgo.Format("2006-01-02"))
	}

	params.Add("end_time", end.Format(time.RFC3339))

	if nextToken != "" {
		params.Add("next_token", nextToken)
		log.Printf("🔄 Using pagination token: %s", nextToken)
	}

	url := fmt.Sprintf("https://api.twitter.com/2/tweets/search/recent?%s", params.Encode())
	log.Printf("🌐 Making API request to Twitter...")

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", rabbithole.Twitter_bearer_token))

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Handle rate limiting
	if resp.StatusCode == 429 {
		mu.Lock()
		// Reset request count and increase wait time
		requestCount = maxRequestsPer15Min // Force a window reset
		mu.Unlock()

		retryAfter := resp.Header.Get("Retry-After")
		waitTime := 30 * time.Second // default to 30 seconds
		if retryAfter != "" {
			if seconds, err := strconv.Atoi(retryAfter); err == nil {
				waitTime = time.Duration(seconds) * time.Second
			}
		}

		log.Printf("⏳ Rate limited. Waiting %v before retry...", waitTime)
		time.Sleep(waitTime)
		return fetchTweetPage(client, handle, start, end, nextToken)
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("⚠️  API Error Response: %s", string(body))
		return nil, "", fmt.Errorf("API returned status code %d: %s", resp.StatusCode, string(body))
	}

	var twitterResp TwitterResponse
	if err := json.Unmarshal(body, &twitterResp); err != nil {
		log.Printf("⚠️  JSON parsing error: %v", err)
		log.Printf("⚠️  Response body: %s", string(body))
		return nil, "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Handle case where no tweets are found
	if len(twitterResp.Data) == 0 {
		log.Printf("ℹ️  No tweets found in this response")
		return []Tweet{}, twitterResp.Meta.NextToken, nil
	}

	authors := make(map[string]string)
	for _, user := range twitterResp.Includes.Users {
		authors[user.ID] = user.Username
	}
	log.Printf("👥 Found %d authors in response", len(authors))

	tweets := make([]Tweet, 0, len(twitterResp.Data))
	for _, t := range twitterResp.Data {
		parsedTime, err := time.Parse(time.RFC3339, t.CreatedAt)
		if err != nil {
			log.Printf("⚠️  Failed to parse time for tweet %s: %v", t.ID, err)
			continue
		}

		tweets = append(tweets, Tweet{
			TweetID:   t.ID,
			Text:      t.Text,
			Username:  authors[t.AuthorID],
			CreatedAt: parsedTime,
		})
	}

	log.Printf("📦 Processed %d tweets from response", len(tweets))
	return tweets, twitterResp.Meta.NextToken, nil
}

func storeTweets(db *gorm.DB, tweets []Tweet) error {
	if len(tweets) == 0 {
		log.Printf("ℹ️  No tweets to store")
		return nil
	}

	log.Printf("💾 Attempting to store %d tweets in database", len(tweets))
	result := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tweet_id"}},
		DoNothing: true,
	}).CreateInBatches(tweets, 100)

	if result.Error != nil {
		return fmt.Errorf("database operation failed: %w", result.Error)
	}

	log.Printf("✅ Successfully stored tweets in database (Rows affected: %d)", result.RowsAffected)
	return nil
}

// FetchTweetsForHandles fetches tweets for multiple Twitter handles
func FetchTweetsForHandles(db *gorm.DB, handles []string, start, end time.Time) error {
	handleDelay := 30 * time.Second // Increased from 10s to 30s

	for _, handle := range handles {
		if err := FetchTweetsForHandle(db, handle, start, end); err != nil {
			log.Printf("❌ Error fetching tweets for @%s: %v", handle, err)
			// Add longer delay after error
			time.Sleep(handleDelay * 2)
			continue
		}
		// Add longer delay between handles
		log.Printf("😴 Waiting between handles...")
		time.Sleep(handleDelay)
	}
	return nil
}

func main() {
	// Set up logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("🚀 Starting Twitter fetcher service")

	db, err := db.InitDB()
	if err != nil {
		log.Fatalf("❌ Failed to initialize database: %v", err)
	}

	// Auto migrate the Tweet model - this will create tweets_v2 table
	log.Println("🔄 Creating/updating tweets_v2 table...")
	if err := db.AutoMigrate(&Tweet{}); err != nil {
		log.Fatalf("❌ Failed to migrate database: %v", err)
	}
	log.Println("✅ Database migration completed")

	// Define CRED handles to fetch
	handles := []string{"CRED_club", "Cred_support"}

	// Calculate time range (last 7 days, as per Twitter API limitation)
	end := time.Now()
	start := end.AddDate(0, 0, -7)

	// Create a channel for error handling
	errChan := make(chan error, 1)

	// Start tweet fetching in a goroutine
	go func() {
		err := FetchTweetsForHandles(db, handles, start, end)
		errChan <- err
	}()

	// Handle program termination
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for either completion or interruption
	select {
	case err := <-errChan:
		if err != nil {
			log.Printf("❌ Error occurred while fetching tweets: %v", err)
		} else {
			log.Println("✅ Successfully completed fetching tweets")
		}
	case sig := <-sigChan:
		log.Printf("📡 Received signal %v, shutting down...", sig)
	}
}
