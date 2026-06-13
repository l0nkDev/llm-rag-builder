package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"llm-rag-builder/internal/models"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

var dbConn *pgx.Conn

func InitDB() error {
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: No .env file found. Proceeding with environment variables.")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("DATABASE_URL is not set")
	}

	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		return fmt.Errorf("unable to connect to database: %w", err)
	}
	dbConn = conn

	schema := `
	CREATE TABLE IF NOT EXISTS materials (
		id SERIAL PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		keywords JSONB NOT NULL
	);

	CREATE TABLE IF NOT EXISTS papers (
		paper_id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		year INT NOT NULL,
		material_id INT REFERENCES materials(id) ON DELETE CASCADE,
		url TEXT,
		pdf_url TEXT,
		has_direct_pdf BOOLEAN DEFAULT FALSE,
		file_size BIGINT,
		pages TEXT,
		word_count INT,
		page_count INT,
		abstract TEXT,
		status TEXT DEFAULT 'BUFFERED',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS authors (
		author_id TEXT PRIMARY KEY,
		name TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS paper_authors (
		paper_id TEXT REFERENCES papers(paper_id) ON DELETE CASCADE,
		author_id TEXT REFERENCES authors(author_id) ON DELETE CASCADE,
		PRIMARY KEY (paper_id, author_id)
	);

	CREATE TABLE IF NOT EXISTS sections (
		id SERIAL PRIMARY KEY,
		paper_id TEXT REFERENCES papers(paper_id) ON DELETE CASCADE,
		header TEXT,
		body TEXT,
		-- Later, we will add 'embedding vector(1536)' here!
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err = dbConn.Exec(context.Background(), schema)
	if err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	log.Println("Database connected and schema initialized.")
	return nil
}

func CloseDB() {
	if dbConn != nil {
		dbConn.Close(context.Background())
	}
}

func SaveKnowledge(paper *models.Paper, knowledge *models.ExtractedKnowledge, materialID int) error {
	ctx := context.Background()

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	paperQuery := `
		INSERT INTO papers (paper_id, title, material_id, url, pdf_url, has_direct_pdf, file_size, pages, word_count, page_count, abstract, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'PROCESSED')
		ON CONFLICT (paper_id) DO UPDATE SET 
			status = 'PROCESSED',
			abstract = EXCLUDED.abstract,
			file_size = EXCLUDED.file_size,
			word_count = EXCLUDED.word_count,
			page_count = EXCLUDED.page_count
	`
	_, err = tx.Exec(ctx, paperQuery, knowledge.PaperID, paper.Title, materialID, paper.URL, paper.PdfUrl, paper.HasDirectPDF, paper.FileSize, paper.Pages, knowledge.WordCount, knowledge.PageCount, knowledge.Abstract)
	if err != nil {
		return fmt.Errorf("failed to insert paper: %w", err)
	}

	sectionQuery := `
		INSERT INTO sections (paper_id, header, body, embedding)
		VALUES ($1, $2, $3, $4::vector)
	`
	for _, sec := range knowledge.Sections {
		if len(sec.Embedding) > 0 {
			embBytes, _ := json.Marshal(sec.Embedding)
			_, err = tx.Exec(ctx, sectionQuery, knowledge.PaperID, sec.Header, sec.Body, string(embBytes))
			if err != nil {
				return fmt.Errorf("failed to insert section '%s': %w", sec.Header, err)
			}
		}
	}

	return tx.Commit(ctx)
}

func SearchDatabase(queryEmbedding []float32) ([]models.SearchResult, error) {
	embBytes, _ := json.Marshal(queryEmbedding)
	queryStr := string(embBytes)

	sqlQuery := `
		SELECT p.paper_id, p.title, s.header, s.body, 1 - (s.embedding <=> $1::vector) AS similarity
		FROM sections s
		JOIN papers p ON s.paper_id = p.paper_id
		ORDER BY s.embedding <=> $1::vector
		LIMIT 3;
	`

	rows, err := dbConn.Query(context.Background(), sqlQuery, queryStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.SearchResult
	for rows.Next() {
		var r models.SearchResult
		if err := rows.Scan(&r.PaperID, &r.Title, &r.Header, &r.Body, &r.Similarity); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func SaveMaterial(name string, keywords []string) (int, error) {
	kwBytes, _ := json.Marshal(keywords)
	var id int
	query := `
		INSERT INTO materials (name, keywords)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET keywords = EXCLUDED.keywords
		RETURNING id
	`
	err := dbConn.QueryRow(context.Background(), query, name, string(kwBytes)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to save material: %w", err)
	}
	return id, nil
}

func SavePaperBuffer(paper *models.Paper, materialID int) (bool, error) {
	ctx := context.Background()
	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Upsert Paper
	paperQuery := `
		INSERT INTO papers (paper_id, title, material_id, url, pdf_url, has_direct_pdf, file_size, pages, abstract, status, year)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'BUFFERED', $10)
		ON CONFLICT (paper_id) DO NOTHING
	`
	tag, err := tx.Exec(ctx, paperQuery, paper.PaperID, paper.Title, materialID, paper.URL, paper.PdfUrl, paper.HasDirectPDF, paper.FileSize, paper.Pages, paper.Abstract, paper.Year)
	if err != nil {
		return false, fmt.Errorf("failed to insert buffered paper: %w", err)
	}
	inserted := tag.RowsAffected() > 0

	// Upsert Authors
	authorQuery := `
		INSERT INTO authors (author_id, name)
		VALUES ($1, $2)
		ON CONFLICT (author_id) DO NOTHING
	`
	linkQuery := `
		INSERT INTO paper_authors (paper_id, author_id)
		VALUES ($1, $2)
		ON CONFLICT (paper_id, author_id) DO NOTHING
	`
	for _, author := range paper.Authors {
		if author.AuthorId == "" {
			continue
		}
		_, err = tx.Exec(ctx, authorQuery, author.AuthorId, author.Name)
		if err != nil {
			return false, fmt.Errorf("failed to insert author: %w", err)
		}
		_, err = tx.Exec(ctx, linkQuery, paper.PaperID, author.AuthorId)
		if err != nil {
			return false, fmt.Errorf("failed to link author: %w", err)
		}
	}

	return inserted, tx.Commit(ctx)
}

func CountUnpaywalledPapers(materialID int) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM papers WHERE material_id = $1 AND has_direct_pdf = true`
	err := dbConn.QueryRow(context.Background(), query, materialID).Scan(&count)
	return count, err
}

func GetPapersMissingPDFUrl() ([]string, error) {
	query := `SELECT paper_id FROM papers WHERE (pdf_url IS NULL OR pdf_url = '') AND has_direct_pdf = true`
	rows, err := dbConn.Query(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func UpdatePDFUrl(paperID, pdfUrl string) error {
	query := `UPDATE papers SET pdf_url = $1 WHERE paper_id = $2`
	_, err := dbConn.Exec(context.Background(), query, pdfUrl, paperID)
	return err
}

func GetBufferedPapers(limit int) ([]models.Paper, error) {
	query := `
		SELECT paper_id, title, material_id, pdf_url, url, has_direct_pdf 
		FROM papers 
		WHERE status = 'BUFFERED' AND pdf_url IS NOT NULL AND pdf_url != ''
		LIMIT $1
	`
	rows, err := dbConn.Query(context.Background(), query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var papers []models.Paper
	for rows.Next() {
		var p models.Paper
		if err := rows.Scan(&p.PaperID, &p.Title, &p.MaterialID, &p.PdfUrl, &p.URL, &p.HasDirectPDF); err != nil {
			return nil, err
		}
		papers = append(papers, p)
	}
	return papers, nil
}

