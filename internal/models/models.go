package models

import "encoding/xml"

type ResearchRequest struct {
	Material   string `json:"material"`
	Product    string `json:"product"`
	MaterialID int    `json:"materialId"`
}

type SemanticScholarResponse struct {
	Total  int     `json:"total"`
	Offset int     `json:"offset"`
	Next   int     `json:"next"`
	Data   []Paper `json:"data"`
}

type Paper struct {
	PaperID       string        `json:"paperId"`
	Title         string        `json:"title"`
	Year          int64         `json:"year"`
	Abstract      string        `json:"abstract"`
	URL           string        `json:"url"`
	OpenAccessPdf OpenAccessPdf `json:"openAccessPdf"`
	ExternalIds   ExternalIds   `json:"externalIds"`
	Authors       []Author      `json:"authors"`
	Pages         string        `json:"pages"`
	HasDirectPDF  bool          `json:"hasDirectPdf"`
	FileSize      int64         `json:"fileSize"`
	PdfUrl        string        `json:"pdfUrl"`
}

type Author struct {
	AuthorId string `json:"authorId"`
	Name     string `json:"name"`
}

type DiscoveryRequest struct {
	Material string `json:"material"`
	Product  string `json:"product"`
	Limit    int    `json:"limit"`
}

type OpenAccessPdf struct {
	URL string `json:"url"`
}

type ExternalIds struct {
	DOI string `json:"DOI"`
}

type UnpaywallResponse struct {
	BestOALocation struct {
		UrlForPdf string `json:"url_for_pdf"`
	} `json:"best_oa_location"`
}

type TEIDocument struct {
	XMLName xml.Name  `xml:"TEI"`
	Header  TEIHeader `xml:"teiHeader"`
	Text    TEIText   `xml:"text"`
}

type TEIHeader struct {
	Abstract struct {
		Paragraphs []string `xml:"p"`
	} `xml:"profileDesc>abstract"`
}

type TEIText struct {
	Body struct {
		Divisions []TEIDivision `xml:"div"`
	} `xml:"body"`
}

type TEIDivision struct {
	Head       string   `xml:"head"`
	Paragraphs []string `xml:"p"`
}

type ExtractedKnowledge struct {
	PaperID  string
	Abstract string
	Sections []Section
}

type Section struct {
	Header    string
	Body      string
	Embedding []float32
}

type SearchRequest struct {
	Query string `json:"query"`
}

type SearchResult struct {
	PaperID    string  `json:"paper_id"`
	Title      string  `json:"title"`
	Header     string  `json:"header"`
	Body       string  `json:"body"`
	Similarity float64 `json:"similarity"`
}
