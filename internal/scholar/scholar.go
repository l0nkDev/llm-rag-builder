package scholar

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"llm-rag-builder/internal/models"
)

var ErrRateLimited = fmt.Errorf("rate limited (429)")

func DiscoverPapers(queryStr string, limit int, offset int) ([]models.Paper, error) {
	query := url.QueryEscape(queryStr)
	apiURL := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/search?query=%s&fields=title,abstract,authors,url,openAccessPdf,externalIds,year&year=2016-&limit=%d&offset=%d", query, limit, offset)

	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("User-Agent", "Amazon-Research-Engine/1.0 (Apprenticeship)")
	if apiKey := os.Getenv("API_KEY"); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var result models.SemanticScholarResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	for i := range result.Data {
		paper := &result.Data[i]
		if paper.ExternalIds.DOI != "" {
			pdfURL, err := GetRealPDFUrl(paper.ExternalIds.DOI)
			if err == nil && pdfURL != "" {
				size, err := GetPDFSize(pdfURL)
				if err == nil {
					paper.HasDirectPDF = true
					paper.FileSize = size
					paper.PdfUrl = pdfURL
				} else {
					// Log that the unpaywall link was dead
					fmt.Printf("   -> Unpaywall link %s returned error: %v\n", pdfURL, err)
				}
			}
		}
	}

	return result.Data, nil
}

func GetPDFSize(pdfURL string) (int64, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("HEAD", pdfURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// Some servers don't like HEAD requests, but most PDF hosts should return 200
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMethodNotAllowed {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return resp.ContentLength, nil
}

func GetRealPDFUrl(doi string) (string, error) {
	if doi == "" {
		return "", fmt.Errorf("no DOI provided")
	}
	email := "rfarell@lonk.dev"
	unpaywallURL := fmt.Sprintf("https://api.unpaywall.org/v2/%s?email=%s", doi, email)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(unpaywallURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Unpaywall returned status %d", resp.StatusCode)
	}
	var result models.UnpaywallResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.BestOALocation.UrlForPdf == "" {
		return "", fmt.Errorf("unpaywall has no direct PDF link")
	}
	return result.BestOALocation.UrlForPdf, nil
}

func GetPaperDetails(paperID string) (*models.Paper, error) {
	apiURL := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=externalIds", paperID)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("User-Agent", "Amazon-Research-Engine/1.0 (Apprenticeship)")
	if apiKey := os.Getenv("API_KEY"); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var paper models.Paper
	if err := json.NewDecoder(resp.Body).Decode(&paper); err != nil {
		return nil, err
	}
	return &paper, nil
}
