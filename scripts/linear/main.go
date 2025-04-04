package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"time"
)

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

func createLinearIssue(apiKey, teamID, title, description string) error {
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
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.linear.app/graphql", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", apiKey) // Linear expects just the token without "Bearer "
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read and parse the response
	var linearResp LinearResponse
	if err := json.NewDecoder(resp.Body).Decode(&linearResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	// Check for GraphQL errors
	if len(linearResp.Errors) > 0 {
		return fmt.Errorf("GraphQL error: %s", linearResp.Errors[0].Message)
	}

	// Check HTTP status
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func main() {
	apiKey := "lin_api_4nhyJjjFfVRW483gOHmQfjFPGHwkGMVFhfXNjVv3"
	teamID := "b35ab586-6384-493d-b8cf-6efd075ff984" // Updated with the correct team ID

	file, err := os.Open("/Users/hardik/Downloads/CRED FAQs - FAQ list.csv")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		panic(err)
	}

	// skip header row
	records = records[1:]

	rand.Seed(time.Now().UnixNano())

	for i := 0; i < 50; i++ {
		record := records[rand.Intn(len(records))]
		lob := record[0]
		category := record[1]
		subcategory := record[2]
		question := record[3]

		title := fmt.Sprintf("[%s] %s - %s", lob, category, question)
		description := fmt.Sprintf("Users are facing issues related to '%s' in '%s > %s'. This needs investigation. Track the frequency and update accordingly.", question, category, subcategory)

		if err := createLinearIssue(apiKey, teamID, title, description); err != nil {
			fmt.Printf("Error creating issue: %v\n", err)
			// Maybe add a retry mechanism here
			continue
		}
		fmt.Printf("Created issue: %s\n", title)

		time.Sleep(500 * time.Millisecond)
	}
}
