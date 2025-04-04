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

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "https://api.linear.app/graphql", bytes.NewBuffer(body))

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

func main() {
	apiKey := "lin_api_4nhyJjjFfVRW483gOHmQfjFPGHwkGMVFhfXNjVv3"
	teamID := "RAB" // extracted from provided URL

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
			fmt.Println("Error creating issue:", err)
		} else {
			fmt.Println("Created issue:", title)
		}

		time.Sleep(500 * time.Millisecond) // avoid hitting API limits
	}
}
