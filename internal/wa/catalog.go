package wa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// WhatsApp Business catalogs. The `w:biz:catalog` IQ that Baileys documents
// is no longer answered by WhatsApp (the socket stays silent until the
// timeout; WhiskeySockets/Baileys#2717). WhatsApp Web reads catalogs with a
// plain HTTPS GraphQL query against graph.whatsapp.com, authenticated only by
// a public app token that ships in its bundle, so this does the same. No
// session is needed: the query works for the account's own catalog and for
// any contact's public catalog.

const (
	// CatalogEndpoint is WhatsApp Web's catalog query endpoint.
	CatalogEndpoint = "https://graph.whatsapp.com/graphql/catalog"
	// CatalogAccessToken is the public app token WhatsApp Web ships (WAWebRelayEnvironment).
	CatalogAccessToken = "WA|787118555984857|7bb1544a3599aa180ac9a3f7688ba243"
	// CatalogDocID is the persisted query id of WAWebQueryCatalog's product catalog query.
	CatalogDocID = "30445081048424116"
	// catalogItemNotFoundCode is what the endpoint answers for an account without a catalog.
	catalogItemNotFoundCode = 2498052
	catalogThumbnailPx      = "100"
	catalogDefaultLimit     = 50
)

// CatalogProduct is one product of a WhatsApp Business catalog.
type CatalogProduct struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	RetailerID          string `json:"retailer_id"`
	URL                 string `json:"url"`
	PriceAmount1000     int64  `json:"price_amount_1000"`
	Price               string `json:"price"`
	SalePriceAmount1000 int64  `json:"sale_price_amount_1000,omitempty"`
	SalePrice           string `json:"sale_price,omitempty"`
	Currency            string `json:"currency"`
	ImageURL            string `json:"image_url"`
	OriginalImageURL    string `json:"original_image_url"`
	Hidden              bool   `json:"hidden"`
	ReviewStatus        string `json:"review_status"`
	Availability        string `json:"availability"`
	MaxAvailable        int64  `json:"max_available,omitempty"`
}

// CatalogPage is one page of a catalog listing. HasCatalog is false when
// WhatsApp knows no catalog for the account (Products is then empty).
type CatalogPage struct {
	JID        string           `json:"jid"`
	HasCatalog bool             `json:"has_catalog"`
	Products   []CatalogProduct `json:"products"`
	NextCursor string           `json:"next_cursor"`
}

// CatalogClient queries WhatsApp Web's catalog endpoint. The zero value uses
// the public endpoint, token and document id with a 30 s HTTP client.
type CatalogClient struct {
	HTTP     *http.Client
	Endpoint string
	Token    string
	DocID    string
}

func (c *CatalogClient) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *CatalogClient) value(field, fallback string) string {
	if field != "" {
		return field
	}
	return fallback
}

// Fetch lists one page of the catalog of jid. limit defaults to 50.
func (c *CatalogClient) Fetch(ctx context.Context, jid types.JID, limit int, after string) (*CatalogPage, error) {
	if limit <= 0 {
		limit = catalogDefaultLimit
	}
	jid = jid.ToNonAD()
	var endpoint, token, docID string
	if c != nil {
		endpoint, token, docID = c.Endpoint, c.Token, c.DocID
	}
	body, err := json.Marshal(CatalogRequest(jid, limit, after, c.value(token, CatalogAccessToken), c.value(docID, CatalogDocID)))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.value(endpoint, CatalogEndpoint), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog endpoint answered HTTP %d", resp.StatusCode)
	}
	page, err := ParseCatalogResponse(raw)
	if err != nil {
		return nil, err
	}
	page.JID = jid.String()
	return page, nil
}

// CatalogRequest is the GraphQL body WhatsApp Web sends (WAWebQueryCatalog).
func CatalogRequest(jid types.JID, limit int, after, token, docID string) map[string]any {
	var cursor any
	if after != "" {
		cursor = after
	}
	return map[string]any{
		"access_token": token,
		"doc_id":       docID,
		"lang":         "en_US",
		"variables": map[string]any{
			"request": map[string]any{
				"product_catalog": map[string]any{
					"jid":                              jid.ToNonAD().String(),
					"allow_shop_source":                "ALLOWSHOPSOURCE_TRUE",
					"width":                            catalogThumbnailPx,
					"height":                           catalogThumbnailPx,
					"limit":                            strconv.Itoa(limit),
					"after":                            cursor,
					"catalog_session_id":               nil,
					"direct_connection_encrypted_info": nil,
					"variant_info_fields":              nil,
					"variant_thumbnail_height":         nil,
					"variant_thumbnail_width":          nil,
				},
			},
		},
	}
}

type catalogResponse struct {
	Data struct {
		Result *struct {
			Typename       string `json:"__typename"`
			ProductCatalog *struct {
				Products []catalogProductWire `json:"products"`
				Paging   struct {
					After string `json:"after"`
				} `json:"paging"`
			} `json:"product_catalog"`
		} `json:"xwa_product_catalog_get_product_catalog"`
	} `json:"data"`
	Errors []struct {
		Code    int    `json:"code"`
		Summary string `json:"summary"`
		Message string `json:"message"`
	} `json:"errors"`
}

type catalogProductWire struct {
	ID                  string          `json:"id"`
	RetailerID          string          `json:"retailer_id"`
	IsHidden            json.RawMessage `json:"is_hidden"`
	ProductAvailability string          `json:"product_availability"`
	MaxAvailable        json.RawMessage `json:"max_available"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	URL                 string          `json:"url"`
	Currency            string          `json:"currency"`
	Price               json.RawMessage `json:"price"`
	StatusInfo          *struct {
		Status string `json:"status"`
	} `json:"status_info"`
	SalePrice *struct {
		Price json.RawMessage `json:"price"`
	} `json:"sale_price"`
	Media *struct {
		Images []struct {
			OriginalImageURL string `json:"original_image_url"`
			RequestImageURL  string `json:"request_image_url"`
		} `json:"images"`
	} `json:"media"`
}

// ParseCatalogResponse reads the GraphQL answer. An "item not found" error
// means the account has no catalog and yields an empty page, not an error.
func ParseCatalogResponse(raw []byte) (*CatalogPage, error) {
	var resp catalogResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("catalog response is not JSON: %w", err)
	}
	page := &CatalogPage{Products: []CatalogProduct{}}
	if resp.Data.Result == nil || resp.Data.Result.ProductCatalog == nil {
		for _, e := range resp.Errors {
			if e.Code == catalogItemNotFoundCode || strings.Contains(strings.ToLower(e.Summary), "not found") {
				return page, nil
			}
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("catalog endpoint error %d: %s", resp.Errors[0].Code, resp.Errors[0].Summary)
		}
		return nil, errors.New("catalog missing from response")
	}
	page.HasCatalog = true
	page.NextCursor = resp.Data.Result.ProductCatalog.Paging.After
	for _, w := range resp.Data.Result.ProductCatalog.Products {
		p := CatalogProduct{
			ID:           w.ID,
			Name:         w.Name,
			Description:  w.Description,
			RetailerID:   w.RetailerID,
			URL:          w.URL,
			Currency:     w.Currency,
			Hidden:       rawTrue(w.IsHidden, "ISHIDDEN_TRUE"),
			Availability: availabilityText(w.ProductAvailability),
			MaxAvailable: rawInt(w.MaxAvailable),
		}
		if amount, ok := rawAmount(w.Price); ok {
			p.PriceAmount1000 = amount
			p.Price = FormatPriceAmount1000(amount)
		}
		if w.SalePrice != nil {
			if amount, ok := rawAmount(w.SalePrice.Price); ok && amount > 0 {
				p.SalePriceAmount1000 = amount
				p.SalePrice = FormatPriceAmount1000(amount)
			}
		}
		if w.StatusInfo != nil {
			p.ReviewStatus = w.StatusInfo.Status
		}
		if w.Media != nil && len(w.Media.Images) > 0 {
			p.ImageURL = w.Media.Images[0].RequestImageURL
			p.OriginalImageURL = w.Media.Images[0].OriginalImageURL
		}
		page.Products = append(page.Products, p)
	}
	return page, nil
}

func availabilityText(raw string) string {
	switch strings.ToUpper(raw) {
	case "", "IN_STOCK":
		return "in stock"
	case "OUT_OF_STOCK":
		return "out of stock"
	default:
		return strings.ToLower(strings.ReplaceAll(raw, "_", " "))
	}
}

// rawAmount reads a thousandths amount sent as a JSON string or number.
func rawAmount(raw json.RawMessage) (int64, bool) {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		return 0, false
	}
	amount, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		if f, ferr := strconv.ParseFloat(text, 64); ferr == nil {
			return int64(f), true
		}
		return 0, false
	}
	return amount, true
}

func rawInt(raw json.RawMessage) int64 {
	amount, _ := rawAmount(raw)
	return amount
}

func rawTrue(raw json.RawMessage, enum string) bool {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return text == "true" || strings.EqualFold(text, enum)
}

// FormatPriceAmount1000 renders a thousandths amount with two decimals
// ("1250.50" for 1250500). Half-thousandths are truncated, never rounded up.
func FormatPriceAmount1000(amount int64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	return fmt.Sprintf("%s%d.%02d", sign, amount/1000, (amount%1000)/10)
}
