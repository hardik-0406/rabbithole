package main

import (
	"fmt"
	"log"

	"github.com/hardik-0406/rabbithole/rabbithole"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func main() {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=disable TimeZone=Asia/Kolkata",
		rabbithole.db_host, rabbithole.db_user, rabbithole.db_pass, rabbithole.db_name, rabbithole.db_port,
	)

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("❌ failed to connect to PostgreSQL:", err)
	}

	// Auto-migrate your Tweet model
	DB.AutoMigrate(&Tweet{})
	log.Println("✅ Connected to PostgreSQL and migrated Tweet table.")
}
