package db

import (
	"fmt"
	"log"
	"time"

	"rabbithole"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// InitDB initializes the database connection
func InitDB() (*gorm.DB, error) {
	log.Println("🔌 Initializing database connection...")

	if DB != nil {
		log.Println("✅ Using existing database connection")
		return DB, nil
	}

	log.Printf("📊 Connecting to PostgreSQL at %s:%s...", rabbithole.DB_host, rabbithole.DB_port)

	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=prefer TimeZone=Asia/Kolkata",
		rabbithole.DB_host, rabbithole.DB_user, rabbithole.DB_pass, rabbithole.DB_name, rabbithole.DB_port,
	)

	config := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Error),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	}

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), config)
	if err != nil {
		log.Printf("❌ Database connection failed: %v", err)
		return nil, fmt.Errorf("failed to connect to DB: %w", err)
	}
	log.Println("✅ Successfully connected to PostgreSQL")

	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("❌ Failed to get underlying *sql.DB: %v", err)
		return nil, fmt.Errorf("failed to get underlying *sql.DB: %w", err)
	}

	log.Println("⚙️  Configuring connection pool...")
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)
	log.Println("✅ Connection pool configured")

	// log.Println("🔄 Running database migrations...")
	// if err := DB.AutoMigrate(&rabbithole.Tweet{}); err != nil {
	// 	log.Printf("❌ Migration failed: %v", err)
	// 	return nil, fmt.Errorf("failed to migrate database: %w", err)
	// }
	// log.Println("✅ Database migrations completed successfully")

	return DB, nil
}
