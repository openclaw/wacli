package wa

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// Shape observed from graph.whatsapp.com on 2026-11-06 (values invented).
const catalogSuccess = `{"data":{"xwa_product_catalog_get_product_catalog":{"__typename":"XWAProductCatalogGetResponseSuccess","product_catalog":{"products":[
{"id":"25623960437298981","retailer_id":"C7115A","is_hidden":null,"is_sanctioned":false,"product_availability":"IN_STOCK","max_available":99,"name":"Cámara + lente","description":"Kit básico","url":"https://example.com/p/1","shimmed_url":"https://l.wl.co/l/?u=x","currency":"PEN","price":"4399000","status_info":{"can_appeal":null,"status":"APPROVED"},"sale_price":{"price":"3699000","start_date":null,"end_date":null},"media":{"images":[{"id":"1","original_image_url":"https://mmg/orig.jpg","request_image_url":"https://mmg/req.jpg"}],"videos":[]},"belongs_to":null,"compliance_category":null,"compliance_info":null,"variant_info":null},
{"id":"2","retailer_id":"","is_hidden":"ISHIDDEN_TRUE","product_availability":"OUT_OF_STOCK","name":"Oculto","description":"","url":"","currency":"PEN","price":1000,"status_info":{"status":"PENDING"},"sale_price":{"price":"0"},"media":{"images":[]}}
],"paging":{"before":"","after":"CURSOR2"}}}},"extensions":{"is_final":true}}`

const catalogNotFound = `{"data":{"xwa_product_catalog_get_product_catalog":null},"errors":[{"message":"A server error field_exception occured.","severity":"CRITICAL","code":2498052,"api_error_code":null,"summary":"Item not found error","description":"Item Not Found Error while accessing this GraphQL endpoint","is_silent":false,"is_transient":false,"path":["xwa_product_catalog_get_product_catalog"]}]}`

func TestParseCatalogResponse(t *testing.T) {
	page, err := ParseCatalogResponse([]byte(catalogSuccess))
	if err != nil {
		t.Fatal(err)
	}
	want := &CatalogPage{HasCatalog: true, NextCursor: "CURSOR2", Products: []CatalogProduct{
		{ID: "25623960437298981", Name: "Cámara + lente", Description: "Kit básico", RetailerID: "C7115A", URL: "https://example.com/p/1", PriceAmount1000: 4399000, Price: "4399.00", SalePriceAmount1000: 3699000, SalePrice: "3699.00", Currency: "PEN", ImageURL: "https://mmg/req.jpg", OriginalImageURL: "https://mmg/orig.jpg", ReviewStatus: "APPROVED", Availability: "in stock", MaxAvailable: 99},
		{ID: "2", Name: "Oculto", Currency: "PEN", PriceAmount1000: 1000, Price: "1.00", Hidden: true, ReviewStatus: "PENDING", Availability: "out of stock"},
	}}
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("got %+v\nwant %+v", page, want)
	}

	empty, err := ParseCatalogResponse([]byte(catalogNotFound))
	if err != nil || empty.HasCatalog || len(empty.Products) != 0 || empty.Products == nil || empty.NextCursor != "" {
		t.Fatalf("%+v %v", empty, err)
	}
	if _, err := ParseCatalogResponse([]byte(`{"data":{"xwa_product_catalog_get_product_catalog":null},"errors":[{"code":1,"summary":"Rate limited"}]}`)); err == nil {
		t.Fatal("other errors must surface")
	}
	if _, err := ParseCatalogResponse([]byte(`{"data":{}}`)); err == nil {
		t.Fatal("missing catalog must surface")
	}
	if _, err := ParseCatalogResponse([]byte(`not json`)); err == nil {
		t.Fatal("bad JSON must surface")
	}
}

func TestCatalogClientFetchSendsWhatsAppWebQuery(t *testing.T) {
	var got map[string]any
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(catalogSuccess))
	}))
	defer server.Close()
	client := &CatalogClient{HTTP: server.Client(), Endpoint: server.URL, Token: "tok", DocID: "doc"}
	page, err := client.Fetch(context.Background(), types.JID{User: "51905448520", Device: 7, Server: types.DefaultUserServer}, 25, "C1")
	if err != nil {
		t.Fatal(err)
	}
	if page.JID != "51905448520@s.whatsapp.net" || !page.HasCatalog || len(page.Products) != 2 {
		t.Fatalf("%+v", page)
	}
	if gotContentType != "application/json" || got["access_token"] != "tok" || got["doc_id"] != "doc" {
		t.Fatalf("%s %+v", gotContentType, got)
	}
	req := got["variables"].(map[string]any)["request"].(map[string]any)["product_catalog"].(map[string]any)
	if req["jid"] != "51905448520@s.whatsapp.net" || req["limit"] != "25" || req["after"] != "C1" || req["allow_shop_source"] != "ALLOWSHOPSOURCE_TRUE" || req["width"] != "100" {
		t.Fatalf("%+v", req)
	}
	for _, key := range []string{"catalog_session_id", "direct_connection_encrypted_info", "variant_info_fields", "variant_thumbnail_height", "variant_thumbnail_width"} {
		if v, ok := req[key]; !ok || v != nil {
			t.Fatalf("%s = %v", key, v)
		}
	}

	// No cursor travels as null; the default limit is 50; HTTP errors surface.
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server2.Close()
	client = &CatalogClient{HTTP: server2.Client(), Endpoint: server2.URL}
	if _, err := client.Fetch(context.Background(), types.NewJID("1", types.DefaultUserServer), 0, ""); err == nil {
		t.Fatal("expected HTTP error")
	}
	req = got["variables"].(map[string]any)["request"].(map[string]any)["product_catalog"].(map[string]any)
	if req["after"] != nil || req["limit"] != "50" || got["access_token"] != CatalogAccessToken || got["doc_id"] != CatalogDocID {
		t.Fatalf("%+v %+v", req, got)
	}
}

func TestFormatPriceAmount1000(t *testing.T) {
	for amount, want := range map[int64]string{0: "0.00", 1000: "1.00", 1250500: "1250.50", 999: "0.99", 5: "0.00", -1500: "-1.50", 4399000: "4399.00"} {
		if got := FormatPriceAmount1000(amount); got != want {
			t.Errorf("%d: got %q want %q", amount, got, want)
		}
	}
}
