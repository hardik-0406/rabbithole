package main

import (
	"log"
	"time"

	"rabbithole"
	"rabbithole/db"
)

var twitterHandles = []string{
	"CRED_club",
	"CRED_support",
}

func main() {
	log.Println("🚀 Starting Twitter data fetching process")
	log.Printf("📋 Will process %d Twitter handles: %v", len(twitterHandles), twitterHandles)

	database, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ Database initialization failed:", err)
	}

	// Change to fetch only last 7 days of tweets
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -7)
	log.Printf("📅 Fetching tweets from %s to %s (last 7 days)",
		startDate.Format("2006-01-02"),
		endDate.Format("2006-01-02"))

	for i, handle := range twitterHandles {
		log.Printf("🎯 Processing handle %d/%d: @%s", i+1, len(twitterHandles), handle)

		if err := rabbithole.FetchTweetsForHandle(database, handle, startDate, endDate); err != nil {
			log.Printf("❌ Error processing @%s: %v", handle, err)
			continue
		}

		if i < len(twitterHandles)-1 {
			log.Printf("😴 Sleeping for 30 seconds to avoid rate limits (%d handles remaining)...",
				len(twitterHandles)-i-1)
			time.Sleep(30 * time.Second)
		}
	}

	log.Println("🎉 Tweet fetching process completed!")
	log.Printf("📊 Processed %d Twitter handles", len(twitterHandles))
}
