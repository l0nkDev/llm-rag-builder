package main

import (
	"flag"
	"log"

	"llm-rag-builder/internal/database"
	"llm-rag-builder/internal/discovery"
)

func main() {
	filePath := flag.String("file", "", "Path to the JSON file containing materials and keywords")
	flag.Parse()

	if *filePath == "" {
		log.Fatal("Please provide a path to a JSON file using the -file flag.")
	}

	// Initialize Database
	if err := database.InitDB(); err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}
	defer database.CloseDB()

	log.Printf("Starting discovery for %s...\n", *filePath)
	
	// Pass target 25 (hardcoded for now as it's the expected target, or we could pass limit but limit is the batch limit. The target is 25).
	discovery.RunDiscovery(*filePath, 25)

	log.Println("\nIngestion complete! All materials and papers have been saved to the buffer database.")
}
