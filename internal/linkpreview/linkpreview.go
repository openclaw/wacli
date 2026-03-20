package linkpreview

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Preview holds Open Graph metadata fetched from a URL.
type Preview struct {
	URL         string
	Title       string
	Description string
	Thumbnail   []byte // JPEG thumbnail data
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"]+`)

// FindURL returns the first HTTP(S) URL found in text, or empty string.
func FindURL(text string) string {
	return urlRe.FindString(text)
}

// Fetch retrieves Open Graph metadata and thumbnail for the given URL.
func Fetch(ctx context.Context, rawURL string) (*Preview, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; WhatsApp/2.23; +http://www.whatsapp.com)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit reading to 1MB to avoid huge pages.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	og := parseOG(string(body))
	if og.Title == "" && og.Description == "" && og.ImageURL == "" {
		return nil, fmt.Errorf("no Open Graph metadata found")
	}

	p := &Preview{
		URL:         rawURL,
		Title:       og.Title,
		Description: og.Description,
	}

	// Fetch thumbnail if available.
	if og.ImageURL != "" {
		thumb, err := fetchThumbnail(ctx, og.ImageURL)
		if err == nil {
			p.Thumbnail = thumb
		}
	}

	return p, nil
}

type ogMeta struct {
	Title       string
	Description string
	ImageURL    string
}

func parseOG(htmlBody string) ogMeta {
	var og ogMeta
	var fallbackTitle string

	tokenizer := html.NewTokenizer(strings.NewReader(htmlBody))
	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			if og.Title == "" {
				og.Title = fallbackTitle
			}
			return og
		case html.StartTagToken, html.SelfClosingTagToken:
			t := tokenizer.Token()
			switch t.Data {
			case "meta":
				var property, content string
				for _, a := range t.Attr {
					switch a.Key {
					case "property", "name":
						property = a.Val
					case "content":
						content = a.Val
					}
				}
				switch property {
				case "og:title":
					if og.Title == "" {
						og.Title = content
					}
				case "og:description":
					if og.Description == "" {
						og.Description = content
					}
				case "og:image":
					if og.ImageURL == "" {
						og.ImageURL = content
					}
				case "description":
					if og.Description == "" {
						og.Description = content
					}
				}
			case "title":
				if tt == html.StartTagToken {
					tokenizer.Next()
					fallbackTitle = strings.TrimSpace(tokenizer.Token().Data)
				}
			}
		}
	}
}

func fetchThumbnail(ctx context.Context, imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; WhatsApp/2.23; +http://www.whatsapp.com)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit thumbnail to 300KB.
	return io.ReadAll(io.LimitReader(resp.Body, 300<<10))
}
