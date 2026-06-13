package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"time"

	"llm-rag-builder/internal/database"
	"llm-rag-builder/internal/scholar"
)

type InputMaterial struct {
	Name     string   `json:"name"`
	Keywords []string `json:"keywords"`
}

func main() {
	filePath := flag.String("file", "", "Path to the JSON file containing materials and keywords")
	limit := flag.Int("limit", 50, "Number of papers to fetch per keyword")
	flag.Parse()

	if *filePath == "" {
		log.Fatal("Please provide a path to a JSON file using the -file flag.")
	}

	// Read and parse JSON
	file, err := os.Open(*filePath)
	if err != nil {
		log.Fatalf("Failed to open file: %v", err)
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("Failed to read file: %v", err)
	}

	var materials []InputMaterial
	if err := json.Unmarshal(bytes, &materials); err != nil {
		log.Fatalf("Failed to parse JSON: %v", err)
	}

	// Initialize Database
	if err := database.InitDB(); err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}
	defer database.CloseDB()

	log.Printf("Starting ingestion for %d materials...\n", len(materials))

	for _, mat := range materials {
		log.Printf("\n--- Processing Material: %s ---\n", mat.Name)
		matID, err := database.SaveMaterial(mat.Name, mat.Keywords)
		if err != nil {
			log.Printf("Failed to save material %s: %v\n", mat.Name, err)
			continue
		}

		target := 25

		existingCount, err := database.CountUnpaywalledPapers(matID)
		if err != nil {
			log.Printf("Failed to count existing papers for %s: %v\n", mat.Name, err)
			continue
		}

		unpaywalledFound := existingCount

		if unpaywalledFound >= target {
			log.Printf("   [Skip] Already have %d unpaywalled papers for material '%s'.\n", unpaywalledFound, mat.Name)
			continue
		}

		for _, keyword := range mat.Keywords {
			if unpaywalledFound >= target {
				break
			}
			offset := 0
			for {
				if unpaywalledFound >= target {
					break
				}
				log.Printf("  -> Querying Semantic Scholar for keyword: '%s' (limit: %d, offset: %d)\n", keyword, *limit, offset)
				papers, err := scholar.DiscoverPapers(keyword, *limit, offset)
				if err != nil {
					log.Printf("     [Error] Failed to fetch papers for '%s': %v\n", keyword, err)
					time.Sleep(2 * time.Second)
					break
				}

				if len(papers) == 0 {
					log.Printf("     No more papers found for keyword '%s'.\n", keyword)
					time.Sleep(2 * time.Second)
					break
				}

				log.Printf("     Found %d papers. Saving unpaywalled to buffer...\n", len(papers))
				for _, paper := range papers {
					if unpaywalledFound >= target {
						break
					}
					if paper.HasDirectPDF {
						inserted, err := database.SavePaperBuffer(&paper, matID)
						if err != nil {
							log.Printf("     [Error] Failed to save buffered paper %s: %v\n", paper.PaperID, err)
						} else if inserted {
							unpaywalledFound++
							log.Printf("     [+] Unpaywalled Paper Saved: %s (%d/%d)\n", paper.Title, unpaywalledFound, target)
						}
					}
				}

				offset += *limit
				// Polite backoff between keyword requests
				time.Sleep(2 * time.Second)
			}
		}

		if unpaywalledFound < target {
			log.Printf("   [Warning] Could only find %d/%d unpaywalled papers for material '%s'.\n", unpaywalledFound, target, mat.Name)
		} else {
			log.Printf("   [Success] Reached target of %d unpaywalled papers for material '%s'.\n", target, mat.Name)
		}
	}

	log.Println("\nIngestion complete! All materials and papers have been saved to the buffer database.")
}
