package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"math/rand"
	"os"
	"rabbithole/db"
	"time"
)

type User struct {
	ID        uint   `gorm:"primaryKey"`
	UUID      string `gorm:"uniqueIndex"`
	FullName  string
	Email     string
	Phone     string
	CreatedAt time.Time
}

type UserAffinity struct {
	ID        uint   `gorm:"primaryKey"`
	UserID    uint   `gorm:"index"`
	LOB       string `gorm:"index"`
	Category  string `gorm:"index"`
	Weight    float64
	CreatedAt time.Time
}

func loadTaxonomy(filePath string) map[string][]string {
	file, err := os.Open(filePath)
	if err != nil {
		log.Fatalf("❌ Failed to open taxonomy CSV: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	_, _ = reader.Read() // skip header

	taxonomy := make(map[string]map[string]bool)
	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		lob := record[0]
		category := record[1]
		if taxonomy[lob] == nil {
			taxonomy[lob] = make(map[string]bool)
		}
		taxonomy[lob][category] = true
	}

	// Flatten into map[string][]string
	flat := make(map[string][]string)
	for lob, cats := range taxonomy {
		for cat := range cats {
			flat[lob] = append(flat[lob], cat)
		}
	}
	return flat
}

func main() {
	dbConn, err := db.InitDB()
	if err != nil {
		log.Fatal("❌ DB connection failed:", err)
	}
	dbConn.AutoMigrate(&UserAffinity{})

	taxonomy := loadTaxonomy("/Users/hardik/Downloads/CRED FAQs - FAQ list.csv")

	var users []User
	if err := dbConn.Find(&users).Error; err != nil {
		log.Fatalf("❌ Failed to load users: %v", err)
	}

	rand.Seed(time.Now().UnixNano())
	var affinities []UserAffinity

	for _, user := range users {
		// Assign 1–3 affinities per user
		numAffinities := rand.Intn(3) + 1
		for i := 0; i < numAffinities; i++ {
			lobs := keys(taxonomy)
			lob := lobs[rand.Intn(len(lobs))]
			categories := taxonomy[lob]
			category := categories[rand.Intn(len(categories))]
			weight := rand.Float64()*0.5 + 0.5 // between 0.5 and 1

			affinities = append(affinities, UserAffinity{
				UserID:    user.ID,
				LOB:       lob,
				Category:  category,
				Weight:    weight,
				CreatedAt: time.Now(),
			})
		}
	}

	if err := dbConn.CreateInBatches(affinities, 100).Error; err != nil {
		log.Fatalf("❌ Failed to insert affinities: %v", err)
	}

	fmt.Printf("✅ Generated %d user affinities for %d users\n", len(affinities), len(users))
}

func keys(m map[string][]string) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	return k
}
