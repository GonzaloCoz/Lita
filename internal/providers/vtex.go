package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/GonzaloCoz/Lita/internal/domain"
	"github.com/GonzaloCoz/Lita/internal/normalize"
)

// VTEXClient queries supermarket VTEX Catalog APIs directly.
type VTEXClient struct {
	httpClient *http.Client
}

func NewVTEXClient(timeout time.Duration) *VTEXClient {
	return &VTEXClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// BuildRegionID encodes "SW#<sellerId>" in base64 as used by VTEX regionalization in Argentina.
func BuildRegionID(sellerID string) string {
	sellerID = strings.TrimSpace(sellerID)
	if sellerID == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte("SW#" + sellerID))
}

type vtexProduct struct {
	ProductID         string     `json:"productId"`
	ProductName       string     `json:"productName"`
	Brand             string     `json:"brand"`
	LinkText          string     `json:"linkText"`
	ProductReference  string     `json:"productReference"`
	Categories        []string   `json:"categories"`
	Items             []vtexItem `json:"items"`
}

type vtexItem struct {
	ItemID  string       `json:"itemId"`
	Name    string       `json:"name"`
	EAN     string       `json:"ean"`
	Images  []vtexImage  `json:"images"`
	Sellers []vtexSeller `json:"sellers"`
}

type vtexImage struct {
	ImageURL string `json:"imageUrl"`
}

type vtexSeller struct {
	SellerID        string        `json:"sellerId"`
	SellerDefault   bool          `json:"sellerDefault"`
	AddToCartLink   string        `json:"addToCartLink"`
	CommertialOffer vtexCommOffer `json:"commertialOffer"`
}

type vtexCommOffer struct {
	Price             float64 `json:"Price"`
	ListPrice         float64 `json:"ListPrice"`
	AvailableQuantity int     `json:"AvailableQuantity"`
	IsAvailable       bool    `json:"IsAvailable"`
}

// SearchText queries the VTEX Catalog Search API directly by search term.
func (c *VTEXClient) SearchText(ctx context.Context, store domain.StoreInfo, query string, sel domain.BranchSelection) ([]domain.Product, error) {
	if store.BaseURL == "" {
		return nil, fmt.Errorf("store %s has no VTEX baseUrl", store.Name)
	}
	base := strings.TrimRight(store.BaseURL, "/")
	escapedQuery := url.PathEscape(strings.TrimSpace(query))

	params := url.Values{}
	params.Set("_from", "0")
	params.Set("_to", "24")
	if store.SalesChannel != "" {
		params.Set("sc", store.SalesChannel)
	}

	sellerID := sel.SellerID
	if sellerID == "" {
		sellerID = store.DefaultSeller
	}

	endpoint := fmt.Sprintf("%s/api/catalog_system/pub/products/search/%s?%s", base, escapedQuery, params.Encode())
	return c.doFetch(ctx, store, endpoint, sellerID)
}

// SearchEAN queries the VTEX Catalog Search API directly by EAN barcode.
func (c *VTEXClient) SearchEAN(ctx context.Context, store domain.StoreInfo, ean string, sel domain.BranchSelection) ([]domain.Product, error) {
	if store.BaseURL == "" {
		return nil, fmt.Errorf("store %s has no VTEX baseUrl", store.Name)
	}
	base := strings.TrimRight(store.BaseURL, "/")

	params := url.Values{}
	params.Set("fq", "alternateIds_Ean:"+strings.TrimSpace(ean))
	if store.SalesChannel != "" {
		params.Set("sc", store.SalesChannel)
	}

	sellerID := sel.SellerID
	if sellerID == "" {
		sellerID = store.DefaultSeller
	}

	endpoint := fmt.Sprintf("%s/api/catalog_system/pub/products/search?%s", base, params.Encode())
	return c.doFetch(ctx, store, endpoint, sellerID)
}

func (c *VTEXClient) doFetch(ctx context.Context, store domain.StoreInfo, endpoint string, sellerID string) ([]domain.Product, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("vtex status %d from %s", resp.StatusCode, store.Name)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	var rawProducts []vtexProduct
	if err := json.Unmarshal(body, &rawProducts); err != nil {
		return nil, err
	}

	base := strings.TrimRight(store.BaseURL, "/")
	out := make([]domain.Product, 0, len(rawProducts))

	for _, rp := range rawProducts {
		if len(rp.Items) == 0 {
			continue
		}
		item := rp.Items[0]

		var chosenSeller *vtexSeller
		if sellerID != "" {
			for i := range item.Sellers {
				if item.Sellers[i].SellerID == sellerID {
					chosenSeller = &item.Sellers[i]
					break
				}
			}
		}
		if chosenSeller == nil {
			for i := range item.Sellers {
				if item.Sellers[i].SellerDefault {
					chosenSeller = &item.Sellers[i]
					break
				}
			}
		}
		if chosenSeller == nil && len(item.Sellers) > 0 {
			chosenSeller = &item.Sellers[0]
		}
		if chosenSeller == nil {
			continue
		}

		offer := chosenSeller.CommertialOffer
		avail := offer.AvailableQuantity > 0 || offer.IsAvailable
		if offer.Price <= 0 && !avail {
			continue
		}

		imgURL := ""
		if len(item.Images) > 0 {
			imgURL = item.Images[0].ImageURL
		}

		link := base
		if rp.LinkText != "" {
			link = fmt.Sprintf("%s/%s/p", base, rp.LinkText)
		}

		normEAN := normalize.NormalizeEAN(item.EAN)
		p := domain.Product{
			ID:             rp.ProductID,
			Name:           rp.ProductName,
			Brand:          rp.Brand,
			Supermarket:    store.Name,
			Price:          offer.Price,
			ListPrice:      offer.ListPrice,
			Link:           link,
			AddToCartLink:  chosenSeller.AddToCartLink,
			Image:          imgURL,
			EAN:            item.EAN,
			NormalizedEAN:  normEAN,
			SKU:            item.ItemID,
			Reference:      rp.ProductReference,
			CategoryPath:   rp.Categories,
			IsAvailable:    avail && offer.Price > 0,
			UnitMeasure:    normalize.ParseUnitMeasure(rp.ProductName, offer.Price),
			ProviderSource: "vtex_direct",
		}
		out = append(out, p)
	}

	return out, nil
}
