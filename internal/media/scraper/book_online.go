package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// ==================== Google Books ====================

// GoogleBooksScraper 通过 Google Books API 刮削书籍
type GoogleBooksScraper struct {
	APIKey        string
	ThumbnailMode string
	ThumbnailPath string
	client        *http.Client
}

func NewGoogleBooksScraper(apiKey, thumbnailMode, thumbnailPath string) *GoogleBooksScraper {
	if thumbnailMode == "" {
		thumbnailMode = "base64"
	}
	if thumbnailPath == "" {
		thumbnailPath = "imgs"
	}
	return &GoogleBooksScraper{
		APIKey:        apiKey,
		ThumbnailMode: thumbnailMode,
		ThumbnailPath: thumbnailPath,
		client:        &http.Client{Timeout: 20 * time.Second},
	}
}

type googleBooksResponse struct {
	Items []struct {
		ID         string `json:"id"`
		VolumeInfo struct {
			Title         string   `json:"title"`
			Authors       []string `json:"authors"`
			Description   string   `json:"description"`
			PublishedDate string   `json:"publishedDate"`
			Publisher     string   `json:"publisher"`
			Categories    []string `json:"categories"`
			AverageRating float64  `json:"averageRating"`
			IndustryIDs   []struct {
				Type       string `json:"type"`
				Identifier string `json:"identifier"`
			} `json:"industryIdentifiers"`
			ImageLinks struct {
				Thumbnail      string `json:"thumbnail"`
				SmallThumbnail string `json:"smallThumbnail"`
			} `json:"imageLinks"`
		} `json:"volumeInfo"`
	} `json:"items"`
}

// ScrapeBook 用 Google Books 填充书籍信息，封面保存为远程链接（由调用方决定是否落盘）
func (s *GoogleBooksScraper) ScrapeBook(item *model.MediaItem) error {
	query := strings.TrimSpace(item.ISBN)
	if query == "" {
		query = strings.TrimSpace(item.ScrapedName)
	}
	if query == "" && item.FileName != "" {
		query = trimExt(item.FileName)
	}
	if query == "" {
		return fmt.Errorf("empty query")
	}
	api := "https://www.googleapis.com/books/v1/volumes?maxResults=1&printType=books&langRestrict=" +
		url.QueryEscape(ScrapeLanguagePrimary()) + "&q=" + url.QueryEscape(query)
	if s.APIKey != "" {
		api += "&key=" + url.QueryEscape(s.APIKey)
	}
	var resp googleBooksResponse
	if err := s.getJSON(api, &resp); err != nil {
		return err
	}
	if len(resp.Items) == 0 {
		return fmt.Errorf("google books: no result for %q", query)
	}
	it := resp.Items[0]
	v := it.VolumeInfo

	if item.ScrapedName == "" {
		item.ScrapedName = v.Title
	}
	if item.Plot == "" {
		item.Plot = v.Description
	}
	if item.Description == "" {
		item.Description = v.Description
	}
	if item.Publisher == "" {
		item.Publisher = v.Publisher
	}
	if item.ReleaseDate == "" {
		item.ReleaseDate = v.PublishedDate
	}
	if item.Authors == "" && len(v.Authors) > 0 {
		item.Authors = toAuthorsJSON(v.Authors)
	}
	if item.Genre == "" && len(v.Categories) > 0 {
		item.Genre = strings.Join(v.Categories, ",")
	}
	if item.ISBN == "" {
		for _, id := range v.IndustryIDs {
			if id.Type == "ISBN_13" || id.Type == "ISBN_10" {
				item.ISBN = id.Identifier
				break
			}
		}
	}
	if item.Rating == 0 && v.AverageRating > 0 {
		item.Rating = float32(v.AverageRating * 2)
	}
	if item.ExternalID == "" {
		item.ExternalID = "google:" + it.ID
	}
	if item.Cover == "" {
		img := v.ImageLinks.Thumbnail
		if img == "" {
			img = v.ImageLinks.SmallThumbnail
		}
		item.Cover = normalizeGoogleImage(img)
	}
	return nil
}

func (s *GoogleBooksScraper) getJSON(api string, out any) error {
	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenList-Media/1.0)")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("google books: unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func normalizeGoogleImage(img string) string {
	if img == "" {
		return ""
	}
	img = strings.Replace(img, "http://", "https://", 1)
	if idx := strings.Index(img, "&edge="); idx > 0 {
		img = img[:idx]
	}
	return img
}

// ==================== OpenLibrary ====================

// OpenLibraryScraper 通过 OpenLibrary Search API 刮削书籍（无需凭据）
type OpenLibraryScraper struct {
	ThumbnailMode string
	ThumbnailPath string
	client        *http.Client
}

func NewOpenLibraryScraper(thumbnailMode, thumbnailPath string) *OpenLibraryScraper {
	if thumbnailMode == "" {
		thumbnailMode = "base64"
	}
	if thumbnailPath == "" {
		thumbnailPath = "imgs"
	}
	return &OpenLibraryScraper{
		ThumbnailMode: thumbnailMode,
		ThumbnailPath: thumbnailPath,
		client:        &http.Client{Timeout: 20 * time.Second},
	}
}

type openLibraryResponse struct {
	Docs []struct {
		Key            string   `json:"key"`
		Title          string   `json:"title"`
		AuthorName     []string `json:"author_name"`
		FirstPublish   int      `json:"first_publish_year"`
		Publisher      []string `json:"publisher"`
		ISBN           []string `json:"isbn"`
		CoverID        int      `json:"cover_i"`
		Subject        []string `json:"subject"`
		RatingsAverage float64  `json:"ratings_average"`
	} `json:"docs"`
}

// ScrapeBook 用 OpenLibrary 填充书籍信息，封面保存为远程链接（由调用方决定是否落盘）
func (s *OpenLibraryScraper) ScrapeBook(item *model.MediaItem) error {
	query := strings.TrimSpace(item.ISBN)
	if query == "" {
		query = strings.TrimSpace(item.ScrapedName)
	}
	if query == "" && item.FileName != "" {
		query = trimExt(item.FileName)
	}
	if query == "" {
		return fmt.Errorf("empty query")
	}
	api := "https://openlibrary.org/search.json?limit=1&fields=key,title,author_name,first_publish_year,publisher,isbn,cover_i,subject,ratings_average&q=" + url.QueryEscape(query)
	var resp openLibraryResponse
	if err := s.getJSON(api, &resp); err != nil {
		return err
	}
	if len(resp.Docs) == 0 {
		return fmt.Errorf("openlibrary: no result for %q", query)
	}
	doc := resp.Docs[0]

	if item.ScrapedName == "" {
		item.ScrapedName = doc.Title
	}
	if item.ReleaseDate == "" && doc.FirstPublish > 0 {
		item.ReleaseDate = fmt.Sprintf("%d", doc.FirstPublish)
	}
	if item.Publisher == "" && len(doc.Publisher) > 0 {
		item.Publisher = doc.Publisher[0]
	}
	if item.Authors == "" && len(doc.AuthorName) > 0 {
		item.Authors = toAuthorsJSON(doc.AuthorName)
	}
	if item.Genre == "" && len(doc.Subject) > 0 {
		limit := len(doc.Subject)
		if limit > 8 {
			limit = 8
		}
		item.Genre = strings.Join(doc.Subject[:limit], ",")
	}
	if item.ISBN == "" && len(doc.ISBN) > 0 {
		item.ISBN = doc.ISBN[0]
	}
	if item.Rating == 0 && doc.RatingsAverage > 0 {
		item.Rating = float32(doc.RatingsAverage * 2)
	}
	if item.ExternalID == "" {
		item.ExternalID = "openlibrary:" + doc.Key
	}
	if item.Cover == "" && doc.CoverID > 0 {
		item.Cover = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", doc.CoverID)
	}
	return nil
}

func (s *OpenLibraryScraper) getJSON(api string, out any) error {
	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenList-Media/1.0)")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openlibrary: unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func toAuthorsJSON(authors []string) string {
	b, err := json.Marshal(authors)
	if err != nil {
		return ""
	}
	return string(b)
}
