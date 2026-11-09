package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GonzaloCoz/Lita/internal/domain"
	"github.com/GonzaloCoz/Lita/internal/normalize"
)

const (
	encomboBaseURL   = "https://encombo.com.ar"
	encomboStoreCode = "openshop_sv"
	encomboStoreID   = "3"
)

var (
	reEncomboURLEAN = regexp.MustCompile(`(?:\b|-)(\d{8}|\d{12,14})\.html`)
	reEncomboImgEAN = regexp.MustCompile(`/(\d{8}|\d{12,14})\.(?:jpg|jpeg|png|webp)`)
)

// EnComboClient queries the Adobe Commerce / Magento 2 Storefront REST API of https://encombo.com.ar.
type EnComboClient struct {
	httpClient *http.Client
}

func NewEnComboClient(timeout time.Duration) *EnComboClient {
	return &EnComboClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type encomboResponse struct {
	Items []encomboItem `json:"items"`
}

type encomboItem struct {
	ID           int              `json:"id"`
	Name         string           `json:"name"`
	Type         string           `json:"type"`
	IsSalable    string           `json:"is_salable"`
	URL          string           `json:"url"`
	CurrencyCode string           `json:"currency_code"`
	PriceInfo    encomboPriceInfo `json:"price_info"`
	Images       []encomboImage   `json:"images"`
}

type encomboPriceInfo struct {
	FinalPrice   domain.FlexFloat `json:"final_price"`
	RegularPrice domain.FlexFloat `json:"regular_price"`
	MinimalPrice domain.FlexFloat `json:"minimal_price"`
}

type encomboImage struct {
	URL    string `json:"url"`
	Code   string `json:"code"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Label  string `json:"label"`
}

var encomboStopWords = map[string]bool{
	"de": true, "la": true, "el": true, "en": true, "con": true,
	"por": true, "para": true, "los": true, "las": true, "del": true,
}

func tokenizeEnComboQuery(q string) []string {
	raw := strings.Fields(strings.ToLower(strings.TrimSpace(q)))
	tokens := make([]string, 0, len(raw))
	for _, w := range raw {
		w = strings.Trim(w, ".,;-_()[]{}\"'")
		if len(w) < 2 || encomboStopWords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	if len(tokens) == 0 && strings.TrimSpace(q) != "" {
		return []string{strings.TrimSpace(q)}
	}
	return tokens
}

// SearchText queries EnCombo's Magento 2 products-render-info REST API by product name tokens.
func (c *EnComboClient) SearchText(ctx context.Context, query string) ([]domain.Product, error) {
	tokens := tokenizeEnComboQuery(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	prods, err := c.fetchByTokens(ctx, tokens)
	if err != nil {
		return nil, err
	}
	// Fallback to primary keyword if multi-word AND filter returned 0 products
	if len(prods) == 0 && len(tokens) > 1 {
		return c.fetchByTokens(ctx, tokens[:1])
	}
	return prods, nil
}

func (c *EnComboClient) fetchByTokens(ctx context.Context, tokens []string) ([]domain.Product, error) {
	params := url.Values{}
	params.Set("storeId", encomboStoreID)
	params.Set("currencyCode", "ARS")
	params.Set("searchCriteria[pageSize]", "25")
	params.Set("searchCriteria[currentPage]", "1")

	groupIdx := 0
	for _, tk := range tokens {
		prefix := fmt.Sprintf("searchCriteria[filterGroups][%d][filters][0]", groupIdx)
		params.Set(prefix+"[field]", "name")
		params.Set(prefix+"[value]", "%"+tk+"%")
		params.Set(prefix+"[conditionType]", "like")
		groupIdx++
	}

	// Only enabled products (status = 1)
	statusPrefix := fmt.Sprintf("searchCriteria[filterGroups][%d][filters][0]", groupIdx)
	params.Set(statusPrefix+"[field]", "status")
	params.Set(statusPrefix+"[value]", "1")
	params.Set(statusPrefix+"[conditionType]", "eq")

	endpoint := fmt.Sprintf("%s/rest/%s/V1/products-render-info?%s", encomboBaseURL, encomboStoreCode, params.Encode())
	return c.doFetch(ctx, endpoint)
}

// SearchEAN queries EnCombo's Magento 2 products-render-info REST API by SKU or URL key containing the EAN barcode.
func (c *EnComboClient) SearchEAN(ctx context.Context, ean string) ([]domain.Product, error) {
	ean = strings.TrimSpace(ean)
	if ean == "" {
		return nil, nil
	}

	params := url.Values{}
	params.Set("storeId", encomboStoreID)
	params.Set("currencyCode", "ARS")
	params.Set("searchCriteria[pageSize]", "10")
	params.Set("searchCriteria[currentPage]", "1")

	// Filter group 0 (OR): sku LIKE %ean% OR url_key LIKE %ean%
	params.Set("searchCriteria[filterGroups][0][filters][0][field]", "sku")
	params.Set("searchCriteria[filterGroups][0][filters][0][value]", "%"+ean+"%")
	params.Set("searchCriteria[filterGroups][0][filters][0][conditionType]", "like")

	params.Set("searchCriteria[filterGroups][0][filters][1][field]", "url_key")
	params.Set("searchCriteria[filterGroups][0][filters][1][value]", "%"+ean+"%")
	params.Set("searchCriteria[filterGroups][0][filters][1][conditionType]", "like")

	// Filter group 1 (AND): status = 1
	params.Set("searchCriteria[filterGroups][1][filters][0][field]", "status")
	params.Set("searchCriteria[filterGroups][1][filters][0][value]", "1")
	params.Set("searchCriteria[filterGroups][1][filters][0][conditionType]", "eq")

	endpoint := fmt.Sprintf("%s/rest/%s/V1/products-render-info?%s", encomboBaseURL, encomboStoreCode, params.Encode())
	return c.doFetch(ctx, endpoint)
}

func (c *EnComboClient) doFetch(ctx context.Context, endpoint string) ([]domain.Product, error) {
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("encombo HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	var parsed encomboResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("encombo JSON decode error: %w", err)
	}

	out := make([]domain.Product, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		if item.IsSalable != "1" && item.IsSalable != "true" {
			continue
		}
		price := float64(item.PriceInfo.FinalPrice)
		if price <= 0 {
			price = float64(item.PriceInfo.MinimalPrice)
		}
		if price <= 0 {
			continue
		}
		listPrice := float64(item.PriceInfo.RegularPrice)
		if listPrice < price {
			listPrice = price
		}

		imgURL := pickEnComboImage(item.Images)
		if imgURL == "" {
			continue
		}

		rawEAN := extractEnComboEAN(item.URL, imgURL)
		normEAN := normalize.NormalizeEAN(rawEAN)

		var discounts []domain.Discount
		if listPrice > price*1.01 {
			pct := int(math.Round(((listPrice - price) / listPrice) * 100))
			if pct > 0 {
				discounts = append(discounts, domain.Discount{
					ID:       fmt.Sprintf("encombo-off-%d", pct),
					Store:    "EnCombo",
					Kind:     "store",
					Headline: fmt.Sprintf("%d%% OFF", pct),
					Benefit: domain.Benefit{
						Type:       "percentage",
						Label:      fmt.Sprintf("%d%% OFF", pct),
						Percentage: domain.FlexFloat(pct),
						UnitPrice:  domain.FlexFloat(price),
					},
				})
			}
		}

		name := strings.TrimSpace(item.Name)
		out = append(out, domain.Product{
			ID:             strconv.Itoa(item.ID),
			Name:           name,
			Supermarket:    "EnCombo",
			Price:          math.Round(price*100) / 100,
			ListPrice:      math.Round(listPrice*100) / 100,
			Link:           item.URL,
			Image:          imgURL,
			EAN:            rawEAN,
			NormalizedEAN:  normEAN,
			SKU:            strconv.Itoa(item.ID),
			IsAvailable:    true,
			Discounts:      discounts,
			UnitMeasure:    normalize.ParseUnitMeasure(name, price),
			ProviderSource: "encombo_magento_rest",
		})
	}

	return out, nil
}

func pickEnComboImage(images []encomboImage) string {
	if len(images) == 0 {
		return ""
	}
	for _, img := range images {
		if img.Width >= 270 && img.URL != "" {
			return img.URL
		}
	}
	return images[0].URL
}

func extractEnComboEAN(productURL, imageURL string) string {
	if m := reEncomboURLEAN.FindStringSubmatch(productURL); len(m) > 1 {
		return m[1]
	}
	if m := reEncomboImgEAN.FindStringSubmatch(imageURL); len(m) > 1 {
		return m[1]
	}
	return ""
}
