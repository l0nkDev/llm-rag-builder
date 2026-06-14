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
	"github.com/ledongthuc/pdf"
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

type InputMaterial struct {
	Name     string   `json:"name"`
	Keywords []string `json:"keywords"`
}

func main() {
	if err := database.InitDB(); err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}
	defer database.CloseDB()
	e := echo.New()

	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())

	go bufferConsumerWorker()
	go discoveryWorker()

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

func logTiming(action, item string, duration time.Duration) {
	f, err := os.OpenFile("timing.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[Error] Failed to open timing.log: %v", err)
		return
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("%s | ITEM: %s | DURATION: %v\n", action, item, duration)
}

func discoveryWorker() {
	for {
		log.Println("[DiscoveryWorker] Starting discovery cycle...")

		file, err := os.Open("materials.json")
		if err != nil {
			log.Printf("[DiscoveryWorker] Failed to open materials.json: %v", err)
			time.Sleep(60 * time.Minute)
			continue
		}

		bytesContent, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			log.Printf("[DiscoveryWorker] Failed to read materials.json: %v", err)
			time.Sleep(60 * time.Minute)
			continue
		}

		var materials []InputMaterial
		if err := json.Unmarshal(bytesContent, &materials); err != nil {
			log.Printf("[DiscoveryWorker] Failed to parse materials.json: %v", err)
			time.Sleep(60 * time.Minute)
			continue
		}

		for _, mat := range materials {
			matID, err := database.SaveMaterial(mat.Name, mat.Keywords)
			if err != nil {
				log.Printf("[DiscoveryWorker] Failed to save material %s: %v", mat.Name, err)
				continue
			}

			target := 25
			existingCount, err := database.CountUnpaywalledPapers(matID)
			if err != nil {
				continue
			}

			unpaywalledFound := existingCount
			if unpaywalledFound >= target {
				continue
			}

			log.Printf("[DiscoveryWorker] Material '%s' needs %d more papers. Discovering...", mat.Name, target-unpaywalledFound)
			startMat := time.Now()

			for _, keyword := range mat.Keywords {
				if unpaywalledFound >= target {
					break
				}
				offset := 0
				limit := 50
				for {
					if unpaywalledFound >= target {
						break
					}
					papers, err := scholar.DiscoverPapers(keyword, limit, offset)
					if err != nil {
						time.Sleep(2 * time.Second)
						break
					}
					if len(papers) == 0 {
						time.Sleep(2 * time.Second)
						break
					}
					for _, paper := range papers {
						if unpaywalledFound >= target {
							break
						}
						if paper.HasDirectPDF {
							inserted, err := database.SavePaperBuffer(&paper, matID)
							if err == nil && inserted {
								unpaywalledFound++
							}
						}
					}
					offset += limit
					time.Sleep(2 * time.Second)
				}
			}
			logTiming("BUFFER_MATERIAL", mat.Name, time.Since(startMat))
		}

		log.Println("[DiscoveryWorker] Discovery cycle complete. Sleeping for 1 hour.")
		time.Sleep(60 * time.Minute)
	}
}

func bufferConsumerWorker() {
	for {
		matID, matName, err := database.GetNextBufferedMaterial()
		if err != nil {
			time.Sleep(30 * time.Second)
			continue
		}

		papers, err := database.GetBufferedPapersForMaterial(matID)
		if err != nil || len(papers) == 0 {
			time.Sleep(30 * time.Second)
			continue
		}

		startMatProcess := time.Now()

		for _, paper := range papers {
			startPaper := time.Now()
			log.Printf("[BufferWorker] Processing Buffered Paper for '%s': %s", matName, paper.Title)
			
			localPath, err := downloadPDF(paper.PdfUrl, paper.PaperID)
			if err != nil {
				log.Printf("   -> Download failed: %v", err)
				continue
			}
			log.Printf("   -> Saved PDF: %s", localPath)

			// Get exact file size
			if stat, err := os.Stat(localPath); err == nil {
				paper.FileSize = stat.Size()
			}

			// Get exact page count
			paper.PageCount = 0
			if f, r, err := pdf.Open(localPath); err == nil {
				paper.PageCount = r.NumPage()
				f.Close()
			}

			log.Printf("   -> Sending %s to GROBID on Ubuntu Server...", paper.PaperID)
			knowledge, err := processWithGrobid(localPath, paper.PaperID)
			if err != nil {
				log.Printf("   -> GROBID extraction failed: %v", err)
				continue
			}

			// Calculate word count
			wordCount := len(bytes.Fields([]byte(knowledge.Abstract)))
			for _, sec := range knowledge.Sections {
				wordCount += len(bytes.Fields([]byte(sec.Body)))
			}
			knowledge.WordCount = wordCount
			knowledge.PageCount = paper.PageCount

			log.Printf("   -> SUCCESS! Extracted %d sections from %s (%d pages, %d words).", len(knowledge.Sections), paper.Title, paper.PageCount, wordCount)
			log.Printf("   -> Generating local embeddings via Ollama...")
			
			for i, sec := range knowledge.Sections {
				textToEmbed := sec.Header + "\n" + sec.Body
				emb, err := generateEmbedding(textToEmbed)
				if err != nil {
					log.Printf("   -> Failed to embed section '%s': %v", sec.Header, err)
					continue
				}
				knowledge.Sections[i].Embedding = emb
			}
			
			log.Printf("   -> Saving to PostgreSQL...")
			err = database.SaveKnowledge(&paper, knowledge, paper.MaterialID)
			if err != nil {
				log.Printf("   -> DB Error: %v", err)
			} else {
				log.Printf("   -> DB Save Complete! ID: %s", paper.PaperID)
			}
			
			logTiming("PROCESS_PAPER", fmt.Sprintf("[%s] %s", matName, paper.Title), time.Since(startPaper))
		}

		logTiming("PROCESS_MATERIAL", matName, time.Since(startMatProcess))
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
