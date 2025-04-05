package secrets

// Database configuration
const (
	DB_HOST     = "rabbithole-instance-1.cdck82weq9q0.ap-south-1.rds.amazonaws.com"
	DB_PORT     = "5432"
	DB_USER     = "rabbithole"
	DB_PASSWORD = "rabbithole" // In production this should be secured
	DB_NAME     = "rabbithole"
)

// API configuration
const (
	API_KEY     = "sk-G_BXXmoaRnY5pkImc2yjDw"
	EMBED_API   = "https://api.rabbithole.cred.club/v1/embeddings"
	CHAT_API    = "https://api.rabbithole.cred.club/v1/chat/completions"
	EMBED_MODEL = "text-embedding-3-small"
	CHAT_MODEL  = "claude-3-7-sonnet"
)
