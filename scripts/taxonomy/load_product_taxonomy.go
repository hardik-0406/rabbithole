// load_taxonomy.go
package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"rabbithole/db"
	"time"
)

type ProductTaxonomy struct {
	ID           uint   `gorm:"primaryKey"`
	LOB          string `gorm:"index"`
	Category     string
	Folder       string
	ArticleTitle string
}

func main() {
	db, err := db.InitDB()
	if err != nil {
		log.Fatal("failed to connect to db:", err)
	}
	db.AutoMigrate(&ProductTaxonomy{})

	file, err := os.Open("/Users/hardik/Downloads/CRED FAQs - FAQ list.csv")
	if err != nil {
		log.Fatal("failed to open CSV file:", err)
	}
	defer file.Close()

	r := csv.NewReader(file)
	r.Read() // skip header

	total := 0
	for {
		record, err := r.Read()
		if err != nil {
			break
		}
		tax := ProductTaxonomy{
			LOB:          record[0],
			Category:     record[1],
			Folder:       record[2],
			ArticleTitle: record[3],
		}
		db.Create(&tax)
		total++
	}

	fmt.Printf("✅ Loaded %d taxonomy records into database\n", total)
	time.Sleep(1 * time.Second)
}
