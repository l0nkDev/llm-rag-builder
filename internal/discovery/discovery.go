package discovery

import (
	"encoding/json"
	"log"
	"os"
	"time"

	"llm-rag-builder/internal/database"
	"llm-rag-builder/internal/scholar"
	"llm-rag-builder/internal/utils"
)

type InputMaterial struct {
	Name     string   `json:"name"`
	Keywords []string `json:"keywords"`
}

func RunDiscovery(jsonFilePath string, target int) {
	bytesContent, err := os.ReadFile(jsonFilePath)
	if err != nil {
		log.Printf("[Discovery] Failed to read %s: %v", jsonFilePath, err)
		return
	}

	var materials []InputMaterial
	if err := json.Unmarshal(bytesContent, &materials); err != nil {
		log.Printf("[Discovery] Failed to parse %s: %v", jsonFilePath, err)
		return
	}

	for _, mat := range materials {
		matID, err := database.SaveMaterial(mat.Name, mat.Keywords)
		if err != nil {
			log.Printf("[Discovery] Failed to save material %s: %v", mat.Name, err)
			continue
		}

		exhausted, err := database.IsMaterialExhausted(matID)
		if err != nil {
			log.Printf("[Discovery] Error checking exhausted state for %s: %v", mat.Name, err)
			continue
		}
		if exhausted {
			log.Printf("[Discovery] Material '%s' is exhausted. Skipping.", mat.Name)
			continue
		}

		existingCount, err := database.CountUnpaywalledPapers(matID)
		if err != nil {
			log.Printf("[Discovery] Error counting papers for %s: %v", mat.Name, err)
			continue
		}

		unpaywalledFound := existingCount
		if unpaywalledFound >= target {
			continue
		}

		log.Printf("[Discovery] Material '%s' needs %d more papers. Discovering...", mat.Name, target-unpaywalledFound)
		startMat := time.Now()

	KeywordLoop:
		for _, keyword := range mat.Keywords {
			if unpaywalledFound >= target {
				break KeywordLoop
			}

			offset := 0
			limit := 50

			for {
				papers, err := scholar.DiscoverPapers(keyword, limit, offset)
				if err != nil {
					log.Printf("[Discovery] Error querying Semantic Scholar for %s: %v", keyword, err)
					time.Sleep(2 * time.Second)
					break // Move to next keyword
				}
				if len(papers) == 0 {
					time.Sleep(2 * time.Second)
					break // No more papers for this keyword
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

				if unpaywalledFound >= target {
					break KeywordLoop
				}

				offset += limit
				time.Sleep(2 * time.Second) // Respect rate limits
			}
		}

		if unpaywalledFound < target {
			log.Printf("[Discovery] Exhausted all keywords for '%s' but only found %d papers. Marking exhausted.", mat.Name, unpaywalledFound)
			database.MarkMaterialExhausted(matID)
		} else {
			log.Printf("[Discovery] Successfully buffered target for '%s'", mat.Name)
		}

		utils.LogTiming("BUFFER_MATERIAL", mat.Name, time.Since(startMat))
	}
}
