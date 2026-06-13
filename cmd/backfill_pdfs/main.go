package main

import (
	"log"
	"time"

	"llm-rag-builder/internal/database"
	"llm-rag-builder/internal/scholar"
)

func main() {
	if err := database.InitDB(); err != nil {
		log.Fatalf("DB Init failed: %v", err)
	}
	defer database.CloseDB()

	paperIDs, err := database.GetPapersMissingPDFUrl()
	if err != nil {
		log.Fatalf("Failed to fetch papers: %v", err)
	}

	log.Printf("Found %d papers missing a pdf_url. Starting backfill...", len(paperIDs))

	for _, paperID := range paperIDs {
		log.Printf("Processing %s...", paperID)
		paper, err := scholar.GetPaperDetails(paperID)
		if err != nil {
			log.Printf("  [Error] Semantic Scholar fetch failed: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if paper.ExternalIds.DOI == "" {
			log.Printf("  [Skip] No DOI found.")
			time.Sleep(2 * time.Second)
			continue
		}

		pdfURL, err := scholar.GetRealPDFUrl(paper.ExternalIds.DOI)
		if err != nil || pdfURL == "" {
			log.Printf("  [Error] Unpaywall fetch failed or no PDF: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		err = database.UpdatePDFUrl(paperID, pdfURL)
		if err != nil {
			log.Printf("  [Error] Failed to update DB: %v", err)
		} else {
			log.Printf("  [Success] Updated PDF URL.")
		}

		time.Sleep(2 * time.Second)
	}
	log.Println("Backfill complete.")
}
