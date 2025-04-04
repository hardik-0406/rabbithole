package main

import (
	"fmt"
	"log"

	"rabbithole"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func main() {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=prefer",
		rabbithole.DB_host, rabbithole.DB_user, rabbithole.DB_pass, rabbithole.DB_name, rabbithole.DB_port,
	)

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("❌ failed to connect to PostgreSQL:", err)
	}

	// Auto-migrate your Tweet model
	DB.AutoMigrate(&rabbithole.Tweet{})
	log.Println("✅ Connected to PostgreSQL and migrated Tweet table.")
}