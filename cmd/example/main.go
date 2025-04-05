package main

import (
	"fmt"
	"os"

	"github.com/cline/cline"
)

func main() {
	// Create a new Cline app
	app := cline.NewApp()
	app.Name = "example"
	app.Usage = "A simple example of using Cline"
	app.Version = "1.0.0"

	// Define commands
	app.Commands = []cline.Command{
		{
			Name:    "greet",
			Aliases: []string{"g"},
			Usage:   "Greet someone",
			Flags: []cline.Flag{
				&cline.StringFlag{
					Name:     "name",
					Aliases:  []string{"n"},
					Usage:    "Name of the person to greet",
					Required: true,
				},
			},
			Action: func(c *cline.Context) error {
				name := c.String("name")
				fmt.Printf("Hello, %s!\n", name)
				return nil
			},
		},
	}

	// Run the application
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
