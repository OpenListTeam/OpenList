package scraper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"golang.org/x/net/html"
)

const (
	doubanSearchURL = "https://www.douban.com/search"
	doubanBookCat   = "1001"
	doubanBase      = "https://book.douban.com/"
)

var (
	doubanBookURLPattern = regexp.MustCompile(`.*/subject/(\d+)/?`)
	doubanDatePattern    = regexp.MustCompile(`(\d{4})-(\d+)`)
	doubanDefaultHeaders = map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Referer":    doubanBase,
	}
)

// DoubanScraper 豆瓣书籍刮削器（移植自 NewDouban.py）
type DoubanScraper struct {
	client *http.Client
	// ThumbnailMode 缩略图存储方式："base64"（默认，存入数据库）或 "local"（存到本地文件）
	ThumbnailMode string
	// ThumbnailPath 本地存储路径，ThumbnailMode 为 "local" 时有效，默认 "/.thumbnail"
	ThumbnailPath string
}

// NewDoubanScraper 创建豆瓣刮削器
func NewDoubanScraper() *DoubanScraper {
	return &DoubanScraper{
		client:        &http.Client{Timeout: 20 * time.Second},
		ThumbnailMode: "base64",
		ThumbnailPath: "/.thumbnail",
	}
}

// NewDoubanScraperWithConfig 创建带完整配置的豆瓣刮削器
func NewDoubanScraperWithConfig(thumbnailMode, thumbnailPath string) *DoubanScraper {
	if thumbnailMode == "" {
		thumbnailMode = "base64"
	}
	if thumbnailPath == "" {
		thumbnailPath = "/.thumbnail"
	}
	return &DoubanScraper{
		client:        &http.Client{Timeout: 20 * time.Second},
		ThumbnailMode: thumbnailMode,
		ThumbnailPath: thumbnailPath,
	}
}

// ScrapeBook 刮削书籍信息
func (s *DoubanScraper) ScrapeBook(item *model.MediaItem) error {
	query := item.ScrapedName
	if query == "" {
		query = strings.TrimSuffix(item.FileName, strings.ToLower(item.FileName[strings.LastIndex(item.FileName, "."):]))
	}

	// 搜索书籍URL列表
	bookURLs, err := s.searchBookURLs(query)
	if err != nil {
		return fmt.Errorf("豆瓣搜索失败: %w", err)
	}
	if len(bookURLs) == 0 {
		return fmt.Errorf("豆瓣未找到匹配书籍: %s", query)
	}

	// 取第一个结果
	bookDetail, err := s.loadBookDetail(bookURLs[0])
	if err != nil {
		return fmt.Errorf("豆瓣获取书籍详情失败: %w", err)
	}

	// 填充字段
	if bookDetail.Title != "" {
		item.ScrapedName = bookDetail.Title
	}
	item.Plot = bookDetail.Description
	// 仅在豆瓣返回了有效封面图片URL时才覆盖本地封面
	// 下载封面图片并根据 ThumbnailMode 存储，避免豆瓣防盗链导致前端无法显示
	if bookDetail.Cover != "" {
		if cover := s.downloadAndStoreCover(item.FilePath, bookDetail.Cover); cover != "" {
			item.Cover = cover
		} else {
			// 下载失败时保留原 URL（降级）
			item.Cover = bookDetail.Cover
		}
	}
	item.Rating = bookDetail.Rating
	item.ReleaseDate = bookDetail.PublishedDate
	item.Publisher = bookDetail.Publisher
	item.ISBN = bookDetail.ISBN
	item.ExternalID = "douban:" + bookDetail.ID

	if len(bookDetail.Authors) > 0 {
		authorsJSON, _ := json.Marshal(bookDetail.Authors)
		item.Authors = string(authorsJSON)
	}
	if len(bookDetail.Tags) > 0 {
		item.Genre = strings.Join(bookDetail.Tags, ",")
	}

	now := time.Now()
	item.ScrapedAt = &now
	return nil
}

type doubanBookDetail struct {
	ID            string
	Title         string
	Authors       []string
	Publisher     string
	PublishedDate string
	Cover         string
	Rating        float32
	Description   string
	Tags          []string
	ISBN          string
}

// searchBookURLs 搜索豆瓣书籍URL列表
func (s *DoubanScraper) searchBookURLs(query string) ([]string, error) {
	searchURL := fmt.Sprintf("%s?cat=%s&q=%s", doubanSearchURL, doubanBookCat, url.QueryEscape(query))
	body, err := s.doGet(searchURL)
	if err != nil {
		return nil, err
	}

	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	var bookURLs []string
	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key == "class" && attr.Val == "nbg" {
					for _, a := range n.Attr {
						if a.Key == "href" {
							parsed := s.calcURL(a.Val)
							if parsed != "" && len(bookURLs) < 5 {
								bookURLs = append(bookURLs, parsed)
							}
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(doc)
	return bookURLs, nil
}

// calcURL 解析豆瓣搜索结果中的真实URL
func (s *DoubanScraper) calcURL(href string) string {
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	rawURL := query.Get("url")
	if rawURL == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(rawURL)
	if err != nil {
		return ""
	}
	if doubanBookURLPattern.MatchString(decoded) {
		return decoded
	}
	return ""
}

// loadBookDetail 加载书籍详情页
func (s *DoubanScraper) loadBookDetail(bookURL string) (*doubanBookDetail, error) {
	body, err := s.doGet(bookURL)
	if err != nil {
		return nil, err
	}
	return s.parseBookHTML(bookURL, string(body))
}

// parseBookHTML 解析书籍HTML（移植自 DoubanBookHtmlParser.parse_book）
func (s *DoubanScraper) parseBookHTML(bookURL, content string) (*doubanBookDetail, error) {
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil, err
	}

	detail := &doubanBookDetail{}

	// 提取豆瓣ID
	if m := doubanBookURLPattern.FindStringSubmatch(bookURL); len(m) > 1 {
		detail.ID = m[1]
	}

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "span":
				// 标题
				if getAttr(n, "property") == "v:itemreviewed" {
					detail.Title = getTextContent(n)
				}
				// 元数据字段
				if getAttr(n, "class") == "pl" {
					text := getTextContent(n)
					tail := getNextText(n)
					switch {
					case strings.HasPrefix(text, "作者") || strings.HasPrefix(text, "译者"):
						// 作者通过链接提取
						collectAuthors(n, &detail.Authors)
					case strings.HasPrefix(text, "出版社"):
						detail.Publisher = strings.TrimSpace(tail)
					case strings.HasPrefix(text, "出版年"):
						detail.PublishedDate = parseDoubanDate(strings.TrimSpace(tail))
					case strings.HasPrefix(text, "ISBN"):
						detail.ISBN = strings.TrimSpace(tail)
					case strings.HasPrefix(text, "副标题"):
						if detail.Title != "" {
							detail.Title += ":" + strings.TrimSpace(tail)
						}
					}
				}
				// 评分
				if getAttr(n, "property") == "v:average" {
					ratingStr := getTextContent(n)
					var rating float32
					fmt.Sscanf(ratingStr, "%f", &rating)
					detail.Rating = rating / 2
				}
			case "img":
				// 封面图片：<img class="nbg" src="..." />
				if getAttr(n, "class") == "nbg" {
					src := getAttr(n, "src")
					if src != "" && !strings.HasSuffix(src, "update_image") {
						detail.Cover = src
					}
				}
				// 备用：#mainpic 下的 img
				if getAttr(n, "id") == "mainpic" || (n.Parent != nil && getAttr(n.Parent, "id") == "mainpic") {
					src := getAttr(n, "src")
					if src != "" && detail.Cover == "" {
						detail.Cover = src
					}
				}
			 case "a":
				// 标签（原封面逻辑移除，改为从img获取）
				cls := getAttr(n, "class")
				if strings.Contains(cls, "tag") {
					tag := getTextContent(n)
					if tag != "" {
						detail.Tags = append(detail.Tags, tag)
					}
				}
			case "div":
				// 简介
				if getAttr(n, "id") == "link-report" {
					detail.Description = extractIntroText(n)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(doc)
	return detail, nil
}

// HTML辅助函数

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func getTextContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

func getNextText(n *html.Node) string {
	if n.NextSibling != nil {
		if n.NextSibling.Type == html.TextNode {
			return strings.TrimSpace(n.NextSibling.Data)
		}
		return getTextContent(n.NextSibling)
	}
	return ""
}

func collectAuthors(n *html.Node, authors *[]string) {
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			href := getAttr(node, "href")
			if strings.Contains(href, "/author") || strings.Contains(href, "/search") {
				name := getTextContent(node)
				if name != "" {
					*authors = append(*authors, name)
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	// 从父节点开始找
	if n.Parent != nil {
		walk(n.Parent)
	}
}

func extractIntroText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "div" {
			if getAttr(node, "class") == "intro" {
				sb.WriteString(getTextContent(node))
				return
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

func parseDoubanDate(dateStr string) string {
	if dateStr == "" {
		return ""
	}
	if m := doubanDatePattern.FindStringSubmatch(dateStr); len(m) > 2 {
		return fmt.Sprintf("%s-%s-01", m[1], m[2])
	}
	return dateStr
}

// downloadAndStoreCover 下载封面图片并根据 ThumbnailMode 存储
// 失败时返回空字符串
func (s *DoubanScraper) downloadAndStoreCover(filePath, imgURL string) string {
	data, mimeType := s.downloadImage(imgURL)
	if len(data) == 0 {
		return ""
	}
	if s.ThumbnailMode == "local" {
		// 本地存储模式
		localScraper := NewBookLocalScraperWithConfig(s.ThumbnailMode, s.ThumbnailPath)
		return localScraper.saveCoverLocal(filePath, data)
	}
	// Base64 模式（默认）
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	thumb := imaging.Resize(img, 300, 0, imaging.Lanczos)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 80}); err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// downloadCoverAsBase64 下载封面图片并转为 data URI Base64 字符串（向后兼容）
// 失败时返回空字符串
func (s *DoubanScraper) downloadCoverAsBase64(imgURL string) string {
	data, mimeType := s.downloadImage(imgURL)
	if len(data) == 0 {
		return ""
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	thumb := imaging.Resize(img, 300, 0, imaging.Lanczos)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 80}); err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// downloadImage 下载图片，返回原始字节和 MIME 类型
func (s *DoubanScraper) downloadImage(imgURL string) ([]byte, string) {
	req, err := http.NewRequest("GET", imgURL, nil)
	if err != nil {
		return nil, ""
	}
	for k, v := range doubanDefaultHeaders {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ""
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) == 0 {
		return nil, ""
	}
	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	return data, mimeType
}

// doGet 发送GET请求
func (s *DoubanScraper) doGet(reqURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range doubanDefaultHeaders {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, reqURL)
	}
	return io.ReadAll(resp.Body)
}
