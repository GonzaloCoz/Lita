package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GonzaloCoz/Lita/internal/domain"
	"github.com/GonzaloCoz/Lita/internal/normalize"
)

// Action IDs reverse-engineered from comprabien.app (module 26922 & 61990).
const (
	ActionVTEXSearch       = "cb7f685212f8db29fb43b5a0106e21100564dd6e"
	ActionVTEXEAN          = "f313c93d9b1ace84e3902e234fa08c2896454099"
	ActionCloudBundle      = "fbf0cbcd9f44b587085752ebf4e4cbfb7b1a195f"
	ActionCotoSearch       = "cdb312f4953e5c1ee8afe5c30dd61563c4309cf7"
	ActionCotoEAN          = "3c6d9a75d75135a8c1eeaff83f24d797686bc17a"
	ActionSuperMamiSearch  = "f33453bc5439627c08b2a6ce637a01284919927e"
	ActionSuperMamiEAN     = "39dcaf2a2611c813164ac7ba8f0116f030993e59"
	ActionMaxiconsumo      = "6fae1a31e6eb42c9fdaa1d82737e08f00b755e5f"
	ActionYaguar           = "d1ff08f900b5a43b08b21836ded165c05e3147a6"
	ActionLaAnonima        = "8374de60f0ade51f674ce9d50ff063b1f9141d9b"
	ActionLiderFarmacias   = "c93616c05279edcf9b32fb901047a0eb897254b2"
	ActionFarmaciaGralPaz  = "8b7b804de3dc9b18afa22100e0b00eab895bbf77"
	ActionSVFarma          = "1ea3305466acf31740261e9c7dc3d8f79d4eacf9"
	ActionSanchezAnt       = "f5ae41b8ab58e7bb7659526f1207c4520de5ae23"
	ActionNewsan           = "bbda09c6b482fd96c34f52dffc4143be855eda91"
	ActionSynonyms         = "14a532b65a4ec2c6f201a706d5fb4b32ffd7cb6b"
)

// CompraBienClient invokes comprabien.app's Next.js Server Actions and REST endpoints.
type CompraBienClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewCompraBienClient(timeout time.Duration) *CompraBienClient {
	return &CompraBienClient{
		baseURL: "https://comprabien.app",
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// CallNextAction executes a Next.js Server Action and extracts the JSON payload from line "1:..." of the React Flight response.
func (c *CompraBienClient) CallNextAction(ctx context.Context, actionID string, args any, target any) error {
	payload, err := json.Marshal(args)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Next-Action", actionID)
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("next-action %s returned status %d", actionID, resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 8*1024*1024))
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 8*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "1:") {
			rawJSON := strings.TrimPrefix(line, "1:")
			return json.Unmarshal([]byte(rawJSON), target)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("no flight data frame (1:) found for action %s", actionID)
}

// SearchCloudBundle fetches Carrefour, MasOnline, Cordiez, and Disco in a single call.
func (c *CompraBienClient) SearchCloudBundle(ctx context.Context, query string, selections map[string]domain.BranchSelection, relevanceFilter bool) (map[string][]domain.Product, error) {
	selMap := make(map[string]any)
	for k, v := range selections {
		if v.SellerID != "" || v.RegionID != "" {
			selMap[k] = v
		}
	}
	args := []any{query, selMap, relevanceFilter}
	var raw map[string][]domain.Product
	if err := c.CallNextAction(ctx, ActionCloudBundle, args, &raw); err != nil {
		return nil, err
	}

	keyMap := map[string]string{
		"disco":     "Disco",
		"carrefour": "Carrefour",
		"changomas": "MasOnline",
		"masonline": "MasOnline",
		"cordiez":   "Cordiez",
	}

	out := make(map[string][]domain.Product, len(raw))
	for k, list := range raw {
		storeName := keyMap[strings.ToLower(k)]
		if storeName == "" {
			storeName = k
		}
		out[storeName] = enrichProducts(list, storeName, "comprabien_cloud")
	}
	return out, nil
}

// SearchStoreText queries a single store by text using the appropriate Server Action.
func (c *CompraBienClient) SearchStoreText(ctx context.Context, store domain.StoreInfo, query string, sel domain.BranchSelection, postalCode string, relevanceFilter bool) ([]domain.Product, error) {
	var products []domain.Product
	var err error

	switch store.Name {
	case "Coto":
		err = c.CallNextAction(ctx, ActionCotoSearch, []any{query, relevanceFilter}, &products)
	case "Super Mami":
		err = c.CallNextAction(ctx, ActionSuperMamiSearch, []any{query, relevanceFilter}, &products)
	case "Maxiconsumo":
		err = c.CallNextAction(ctx, ActionMaxiconsumo, []any{query, relevanceFilter}, &products)
	case "Yaguar":
		err = c.CallNextAction(ctx, ActionYaguar, []any{query, sel.SellerID, relevanceFilter}, &products)
	case "La Anónima":
		cp := postalCode
		if cp == "" {
			cp = "5000"
		}
		err = c.CallNextAction(ctx, ActionLaAnonima, []any{query, cp, relevanceFilter}, &products)
	case "Lider Farmacias":
		err = c.CallNextAction(ctx, ActionLiderFarmacias, []any{query, relevanceFilter}, &products)
	case "Farmacia Gral. Paz":
		err = c.CallNextAction(ctx, ActionFarmaciaGralPaz, []any{query, relevanceFilter}, &products)
	case "SV Farma":
		err = c.CallNextAction(ctx, ActionSVFarma, []any{query, relevanceFilter}, &products)
	case "Sanchez Antoniolli":
		err = c.CallNextAction(ctx, ActionSanchezAnt, []any{query, relevanceFilter}, &products)
	default:
		// VTEX generic action
		if store.BaseURL == "" {
			return nil, fmt.Errorf("unsupported store %s", store.Name)
		}
		var selArg any
		if sel.SellerID != "" || sel.RegionID != "" {
			selArg = sel
		} else {
			selArg = map[string]string{}
		}
		err = c.CallNextAction(ctx, ActionVTEXSearch, []any{store.Name, store.BaseURL, query, selArg, relevanceFilter}, &products)
	}

	if err != nil {
		return nil, err
	}
	return enrichProducts(products, store.Name, "comprabien_action"), nil
}

// SearchStoreEAN queries a single store by EAN-8/EAN-13 barcode.
func (c *CompraBienClient) SearchStoreEAN(ctx context.Context, store domain.StoreInfo, ean string, sel domain.BranchSelection) ([]domain.Product, error) {
	var products []domain.Product
	var err error

	switch store.Name {
	case "Coto":
		err = c.CallNextAction(ctx, ActionCotoEAN, []any{ean}, &products)
	case "Super Mami":
		err = c.CallNextAction(ctx, ActionSuperMamiEAN, []any{ean}, &products)
	case "Lider Farmacias":
		err = c.CallNextAction(ctx, ActionLiderFarmacias, []any{ean, false}, &products)
	case "Sanchez Antoniolli":
		err = c.CallNextAction(ctx, ActionSanchezAnt, []any{ean, false}, &products)
	default:
		if !store.SupportsEAN || store.Engine != "vtex" {
			return nil, fmt.Errorf("store %s does not support EAN search", store.Name)
		}
		var selArg any
		if sel.SellerID != "" || sel.RegionID != "" {
			selArg = sel
		}
		err = c.CallNextAction(ctx, ActionVTEXEAN, []any{store.Name, ean, selArg}, &products)
	}

	if err != nil {
		return nil, err
	}
	return enrichProducts(products, store.Name, "comprabien_ean"), nil
}

// ResolveBranches resolves supermarket branch sellerIds and regionIds by postalCode or coordinates.
func (c *CompraBienClient) ResolveBranches(ctx context.Context, postalCode string, lat, lng *float64) (map[string]any, error) {
	bodyMap := make(map[string]any)
	if postalCode != "" {
		bodyMap["postalCode"] = postalCode
	}
	if lat != nil && lng != nil {
		bodyMap["lat"] = *lat
		bodyMap["lng"] = *lng
	}
	payload, _ := json.Marshal(bodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/branch-sellers", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

// FetchDiscounts fetches the active bank/wallet/club discount rules.
func (c *CompraBienClient) FetchDiscounts(ctx context.Context) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/discounts", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func enrichProducts(list []domain.Product, storeName string, source string) []domain.Product {
	out := make([]domain.Product, 0, len(list))
	for _, p := range list {
		if p.Name == "" || (p.Price <= 0 && !p.IsAvailable) {
			continue
		}
		if p.Supermarket == "" {
			p.Supermarket = storeName
		}
		if p.ListPrice <= 0 {
			p.ListPrice = p.Price
		}
		p.NormalizedEAN = normalize.NormalizeEAN(p.EAN)
		p.UnitMeasure = normalize.ParseUnitMeasure(p.Name, p.Price)
		p.ProviderSource = source
		out = append(out, p)
	}
	return out
}
