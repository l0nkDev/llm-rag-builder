package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"llm-rag-builder/internal/database"
	"llm-rag-builder/internal/models"
	"llm-rag-builder/internal/scholar"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// --- CONFIGURATION ---
const (
	DownloadDir = "downloads"
	// Change this to your HP Pavilion's actual IP
	GrobidEndpoint = "http://localhost:8070/api/processFulltextDocument"
	BaseWaitTime   = 5 * time.Second
	MaxWaitTime    = 1 * time.Minute
)

var ErrRateLimited = errors.New("rate limited (429)")

var researchQueue = make(chan models.ResearchRequest, 100)

func main() {
	if err := database.InitDB(); err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}
	defer database.CloseDB()
	e := echo.New()

	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())

	for i := 1; i <= 1; i++ {
		go ingestionWorker(i, researchQueue)
	}

	e.POST("/api/v1/research", triggerResearch)
	e.POST("/api/v1/search", handleSearch)
	e.POST("/api/v1/discover", handleDiscovery)

	e.Logger.Fatal(e.Start(":8080"))
}

func triggerResearch(c echo.Context) error {
	var req models.ResearchRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid payload"})
	}

	if req.MaterialID == 0 {
		matID, err := database.SaveMaterial(req.Material, []string{req.Material})
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save material"})
		}
		req.MaterialID = matID
	}
	select {
	case researchQueue <- req:
		return c.JSON(http.StatusAccepted, map[string]string{
			"message": fmt.Sprintf("Research queued for %s %s", req.Material, req.Product),
		})
	default:
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "Queue is full"})
	}
}

func handleSearch(c echo.Context) error {
	var req models.SearchRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid payload"})
	}
	queryVector, err := generateEmbedding(req.Query)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to embed query"})
	}
	results, err := database.SearchDatabase(queryVector)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Database search failed"})
	}
	return c.JSON(http.StatusOK, results)
}

func handleDiscovery(c echo.Context) error {
	var req models.DiscoveryRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid payload"})
	}
	if req.Limit <= 0 {
		req.Limit = 10 // default limit
	}
	papers, err := scholar.DiscoverPapers(req.Material+" "+req.Product, req.Limit, 0)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to discover papers"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"count":  len(papers),
		"papers": papers,
	})
}

func ingestionWorker(id int, jobs <-chan models.ResearchRequest) {
	currentWait := BaseWaitTime
	for job := range jobs {
		time.Sleep(currentWait)
		log.Printf("[Worker %d] Researching: %s (%s)\n", id, job.Material, job.Product)
		papers, err := scholar.DiscoverPapers(job.Material+" "+job.Product, 3, 0)
		if errors.Is(err, scholar.ErrRateLimited) {
			log.Printf("[Worker %d] 429 Hit! Backing off. New wait: %v\n", id, currentWait)
			currentWait *= 2
			if currentWait > MaxWaitTime {
				currentWait = MaxWaitTime
			}
			go func(j models.ResearchRequest) { researchQueue <- j }(job)
			continue
		}
		if err != nil {
			log.Printf("[Worker %d] Fetch error: %v\n", id, err)
			continue
		}
		if currentWait > BaseWaitTime {
			currentWait -= 1 * time.Second
		}
		for _, paper := range papers {
			log.Printf("[Worker %d] Found Paper: %s\n", id, paper.Title)
			if paper.ExternalIds.DOI != "" {
				log.Printf("   -> Resolving DOI via Unpaywall: %s\n", paper.ExternalIds.DOI)
				pdfURL, err := scholar.GetRealPDFUrl(paper.ExternalIds.DOI)
				if err != nil {
					log.Printf("   -> Could not resolve direct PDF: %v\n", err)
					continue
				}
				log.Printf("   -> Direct PDF Found! Downloading from: %s\n", pdfURL)
				localPath, err := downloadPDF(pdfURL, paper.PaperID)
				if err != nil {
					log.Printf("   -> Download failed: %v\n", err)
					continue
				}
				log.Printf("   -> Saved PDF: %s\n", localPath)
				log.Printf("   -> Sending %s to GROBID on Ubuntu Server...\n", paper.PaperID)
				knowledge, err := processWithGrobid(localPath, paper.PaperID)
				if err != nil {
					log.Printf("   -> GROBID extraction failed: %v\n", err)
					continue
				}
				log.Printf("   -> SUCCESS! Extracted %d sections from %s.\n", len(knowledge.Sections), paper.Title)
				log.Printf("   -> Generating local embeddings via Ollama...\n")
				for i, sec := range knowledge.Sections {
					textToEmbed := sec.Header + "\n" + sec.Body
					emb, err := generateEmbedding(textToEmbed)
					if err != nil {
						log.Printf("   -> Failed to embed section '%s': %v\n", sec.Header, err)
						continue
					}
					knowledge.Sections[i].Embedding = emb
				}
				log.Printf("   -> Saving to PostgreSQL...\n")
				err = database.SaveKnowledge(&paper, knowledge, job.MaterialID)
				if err != nil {
					log.Printf("   -> DB Error: %v\n", err)
				} else {
					log.Printf("   -> DB Save Complete! ID: %s\n", paper.PaperID)
				}

			} else {
				log.Printf("   -> No DOI available for resolving PDF.\n")
			}
		}
		log.Printf("[Worker %d] Finished task for %s\n", id, job.Material)
	}
}

func downloadPDF(targetURL string, paperID string) (string, error) {
	if err := os.MkdirAll(DownloadDir, os.ModePerm); err != nil {
		return "", err
	}
	filePath := filepath.Join(DownloadDir, paperID+".pdf")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(targetURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return filePath, err
}

func processWithGrobid(filePath string, paperID string) (*models.ExtractedKnowledge, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("input", filePath)
	io.Copy(part, file)
	writer.Close()

	grobidURL := "http://localhost:8070/api/processFulltextDocument"
	req, _ := http.NewRequest("POST", grobidURL, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GROBID network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GROBID returned status: %d", resp.StatusCode)
	}

	var tei models.TEIDocument
	if err := xml.NewDecoder(resp.Body).Decode(&tei); err != nil {
		return nil, fmt.Errorf("failed to parse TEI XML: %w", err)
	}

	knowledge := &models.ExtractedKnowledge{
		PaperID: paperID,
	}

	for _, p := range tei.Header.Abstract.Paragraphs {
		knowledge.Abstract += p + "\n"
	}

	for _, div := range tei.Text.Body.Divisions {
		if len(div.Paragraphs) == 0 {
			continue
		}

		sectionBody := ""
		for _, p := range div.Paragraphs {
			sectionBody += p + "\n"
		}

		knowledge.Sections = append(knowledge.Sections, models.Section{
			Header: div.Head,
			Body:   sectionBody,
		})
	}

	return knowledge, nil
}

func generateEmbedding(text string) ([]float32, error) {
	payload := map[string]interface{}{
		"model": "nomic-embed-text",
		"input": text,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post("http://localhost:11434/api/embed", "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("ollama connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return result.Embeddings[0], nil
}
