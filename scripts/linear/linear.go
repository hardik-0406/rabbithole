package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"gorm.io/gorm"
)

const (
	linearAPIEndpoint = "https://api.linear.app/graphql"
	linearAPIKey      = "lin_api_4nhyJjjFfVRW483gOHmQfjFPGHwkGMVFhfXNjVv3"
	teamID            = "b35ab586-6384-493d-b8cf-6efd075ff984"
)

type LinearService struct {
	db *gorm.DB
}

type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type LinearResponse struct {
	Data struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				URL   string `json:"url"`
			} `json:"issue"`
		} `json:"issueCreate"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type GenerateTicketsResponse struct {
	TicketsCreated int      `json:"tickets_created"`
	TicketIDs      []string `json:"ticket_ids"`
	Status         string   `json:"status"`
}

func NewLinearService(db *gorm.DB) (*LinearService, error) {
	return &LinearService{
		db: db,
	}, nil
}

func (s *LinearService) GenerateTickets(ctx context.Context) (*GenerateTicketsResponse, error) {
	log.Println("🎫 Starting ticket generation from taxonomy")

	// Query to get random taxonomy entries
	var taxonomyEntries []struct {
		LOB         string
		Category    string
		Subcategory string
		Question    string
	}

	query := `
		SELECT DISTINCT lob, category, folder as subcategory, title as question
		FROM taxonomy_embeddings
		WHERE lob != '' AND category != ''
		ORDER BY RANDOM()
		LIMIT 20
	`

	if err := s.db.Raw(query).Scan(&taxonomyEntries).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch taxonomy entries: %w", err)
	}

	var ticketIDs []string
	ticketsCreated := 0

	for _, entry := range taxonomyEntries {
		title := fmt.Sprintf("[%s] %s - %s", entry.LOB, entry.Category, entry.Question)
		description := fmt.Sprintf("Users are facing issues related to '%s' in '%s > %s'. This needs investigation. Track the frequency and update accordingly.",
			entry.Question, entry.Category, entry.Subcategory)

		ticketID, err := s.createLinearIssue(title, description)
		if err != nil {
			log.Printf("Error creating issue: %v\n", err)
			continue
		}

		ticketIDs = append(ticketIDs, ticketID)
		ticketsCreated++

		// Rate limiting
		time.Sleep(500 * time.Millisecond)
	}

	return &GenerateTicketsResponse{
		TicketsCreated: ticketsCreated,
		TicketIDs:      ticketIDs,
		Status:         "success",
	}, nil
}

func (s *LinearService) createLinearIssue(title, description string) (string, error) {
	query := `
		mutation IssueCreate($input: IssueCreateInput!) {
			issueCreate(input: $input) {
				success
				issue {
					id
					title
					url
				}
			}
		}
	`

	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"teamId":      teamID,
			"title":       title,
			"description": description,
		},
	}

	payload := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", linearAPIEndpoint, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", linearAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	var linearResp LinearResponse
	if err := json.NewDecoder(resp.Body).Decode(&linearResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(linearResp.Errors) > 0 {
		return "", fmt.Errorf("GraphQL error: %s", linearResp.Errors[0].Message)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return linearResp.Data.IssueCreate.Issue.ID, nil
}
