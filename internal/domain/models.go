package domain

import (
	"encoding/json"
	"strconv"
	"strings"
)

// FlexFloat unmarshals both JSON numbers and numeric strings gracefully.
type FlexFloat float64

func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		s = strings.Trim(s, "\"")
		s = strings.TrimSpace(strings.TrimPrefix(s, "$"))
		// Handle Argentine format if needed or standard float
		if strings.Contains(s, ",") && strings.Contains(s, ".") {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.ReplaceAll(s, ",", ".")
		} else if strings.Contains(s, ",") {
			s = strings.ReplaceAll(s, ",", ".")
		}
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			*f = FlexFloat(v)
		}
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err == nil {
		*f = FlexFloat(v)
	}
	return nil
}

// StoreCategory defines the vertical of a store.
type StoreCategory string

const (
	CategorySupermercados StoreCategory = "supermercados"
	CategoryFarmacias     StoreCategory = "farmacias"
	CategoryElectronica   StoreCategory = "electronica"
	CategoryVarios        StoreCategory = "varios"
)

// StoreInfo describes a retail chain supported by Lita.
type StoreInfo struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Category      StoreCategory `json:"category"`
	Engine        string        `json:"engine"` // "vtex" | "scraper"
	BaseURL       string        `json:"baseUrl,omitempty"`
	DefaultSeller string        `json:"defaultSeller,omitempty"`
	SalesChannel  string        `json:"salesChannel,omitempty"`
	SupportsEAN   bool          `json:"supportsEan"`
	BadgeColor    string        `json:"badgeColor"`
	EnabledByDef  bool          `json:"enabledByDefault"`
}

// Benefit represents the structured discount benefit (e.g. multibuy 3x2, percentage 25%).
type Benefit struct {
	Type        string    `json:"type"`                  // "multibuy" | "percentage" | "second_unit" | "fixed"
	Label       string    `json:"label,omitempty"`       // e.g. "3x2", "2da al 70%"
	MinQty      FlexFloat `json:"minQty,omitempty"`      // e.g. 3
	UnitPrice   FlexFloat `json:"unitPrice,omitempty"`   // effective unit price under promo
	Percentage  FlexFloat `json:"percentage,omitempty"`  // e.g. 25
	SecondUnit  FlexFloat `json:"secondUnit,omitempty"`  // e.g. 70
}

// Discount represents a store or payment/bank promotion attached to a product.
type Discount struct {
	ID             string  `json:"id"`
	Store          string  `json:"store"`
	Kind           string  `json:"kind"` // "store" | "bank" | "wallet" | "club"
	Benefit        Benefit `json:"benefit"`
	Headline       string  `json:"headline"`
	ConditionsText string  `json:"conditionsText,omitempty"`
	Channel        string  `json:"channel,omitempty"`
}

// UnitMeasure holds the parsed net weight/volume and calculated reference price ($/kg or $/L).
type UnitMeasure struct {
	Kind           string  `json:"kind"`           // "volume" | "weight"
	UnitAmount     float64 `json:"unitAmount"`     // in ml or g per single unit
	Pack           int     `json:"pack"`           // number of units in pack (default 1)
	TotalAmount    float64 `json:"totalAmount"`    // UnitAmount * Pack (in ml or g)
	Label          string  `json:"label"`          // e.g. "1 L", "500 g", "473 ml x 6"
	ReferencePrice float64 `json:"referencePrice"` // Price per 1 Kg or 1 Liter
	ReferenceUnit  string  `json:"referenceUnit"`  // "L" or "kg"
}

// Product is the normalized product representation across all stores.
type Product struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	Brand                string       `json:"brand,omitempty"`
	Supermarket          string       `json:"supermarket"`
	Price                float64      `json:"price"`
	ListPrice            float64      `json:"listPrice"`
	PriceWithoutMultiBuy FlexFloat    `json:"priceWithoutMultiBuy,omitempty"`
	MultiBuyPrice        FlexFloat    `json:"multiBuyPrice,omitempty"`
	MultiBuyMinQty       FlexFloat    `json:"multiBuyMinQty,omitempty"`
	Link                 string       `json:"link"`
	AddToCartLink        string       `json:"addToCartLink,omitempty"`
	Image                string       `json:"image"`
	EAN                  string       `json:"ean,omitempty"`
	NormalizedEAN        string       `json:"normalizedEan,omitempty"`
	SKU                  string       `json:"sku,omitempty"`
	Reference            string       `json:"reference,omitempty"`
	CategoryPath         []string     `json:"categoryPath,omitempty"`
	Discounts            []Discount   `json:"discounts,omitempty"`
	IsAvailable          bool         `json:"isAvailable"`
	RelevanceScore       float64      `json:"relevanceScore,omitempty"`
	ProximityScore       float64      `json:"proximityScore,omitempty"`
	UnitMeasure          *UnitMeasure `json:"unitMeasure,omitempty"`
	ProviderSource       string       `json:"providerSource,omitempty"` // "vtex_direct" | "comprabien_action"
}

// StoreOffer represents one store's offer inside a grouped product comparison.
type StoreOffer struct {
	Store       string   `json:"store"`
	Price       float64  `json:"price"`
	ListPrice   float64  `json:"listPrice"`
	IsAvailable bool     `json:"isAvailable"`
	DiffPct     float64  `json:"diffPct"` // % difference vs cheapest offer in group
	Product     Product  `json:"product"`
}

// ProductGroup groups identical products (by normalized EAN) across multiple stores.
type ProductGroup struct {
	GroupKey       string       `json:"groupKey"` // "ean:<normalized>" or "<store>:<id>"
	EAN            string       `json:"ean,omitempty"`
	Title          string       `json:"title"`
	Brand          string       `json:"brand,omitempty"`
	Image          string       `json:"image"`
	UnitMeasure    *UnitMeasure `json:"unitMeasure,omitempty"`
	BestPrice      float64      `json:"bestPrice"`
	BestStore      string       `json:"bestStore"`
	MaxPrice       float64      `json:"maxPrice"`
	AvgPrice       float64      `json:"avgPrice"`
	MaxSavingPct   float64      `json:"maxSavingPct"` // % saved buying at BestStore vs MaxPrice
	StoreCount     int          `json:"storeCount"`
	Offers         []StoreOffer `json:"offers"` // sorted by Price ascending
}

// StoreTiming tracks the latency and status of each store in a fan-out search.
type StoreTiming struct {
	Store      string `json:"store"`
	Status     string `json:"status"` // "ok" | "cached" | "empty" | "timeout" | "error"
	Count      int    `json:"count"`
	DurationMs int64  `json:"durationMs"`
	Source     string `json:"source,omitempty"`
	Error      string `json:"error,omitempty"`
}

// SearchResponse is returned by GET /api/search and GET /api/ean/{ean}.
type SearchResponse struct {
	Query        string               `json:"query"`
	ExpandedFrom string               `json:"expandedFrom,omitempty"`
	Mode         string               `json:"mode"` // "text" | "ean"
	TotalGroups  int                  `json:"totalGroups"`
	TotalItems   int                  `json:"totalItems"`
	DurationMs   int64                `json:"durationMs"`
	Groups       []ProductGroup       `json:"groups"`
	ByStore      map[string][]Product `json:"byStore"`
	StoreTimings []StoreTiming        `json:"storeTimings"`
}

// BranchSelection holds the selected sellerId and regionId for a specific store.
type BranchSelection struct {
	SellerID string `json:"sellerId,omitempty"`
	RegionID string `json:"regionId,omitempty"`
}

// CartItem represents a product added to the user's shopping cart.
type CartItem struct {
	GroupKey string       `json:"groupKey"`
	EAN      string       `json:"ean,omitempty"`
	Title    string       `json:"title"`
	Brand    string       `json:"brand,omitempty"`
	Image    string       `json:"image,omitempty"`
	Quantity int          `json:"quantity"`
	Offers   []StoreOffer `json:"offers,omitempty"`
}

// CartCompareRequest is the payload for POST /api/cart/compare.
type CartCompareRequest struct {
	Items          []CartItem `json:"items"`
	Stores         []string   `json:"stores,omitempty"`
	ResolveMissing bool       `json:"resolveMissing,omitempty"`
}

// StoreCartLine represents how much a specific cart item costs in a specific store.
type StoreCartLine struct {
	GroupKey         string  `json:"groupKey"`
	EAN              string  `json:"ean,omitempty"`
	Title            string  `json:"title"`
	Quantity         int     `json:"quantity"`
	Available        bool    `json:"available"`
	UnitPrice        float64 `json:"unitPrice,omitempty"`
	ListPrice        float64 `json:"listPrice,omitempty"`
	Subtotal         float64 `json:"subtotal,omitempty"`
	ListSubtotal     float64 `json:"listSubtotal,omitempty"`
	Link             string  `json:"link,omitempty"`
	Image            string  `json:"image,omitempty"`
	IsCheapestForRow bool    `json:"isCheapestForRow"`
}

// StoreCartSummary summarizes the total cost and coverage of buying the cart at a single store.
type StoreCartSummary struct {
	Store              string          `json:"store"`
	BadgeColor         string          `json:"badgeColor,omitempty"`
	AvailableCount     int             `json:"availableCount"`
	MissingCount       int             `json:"missingCount"`
	TotalItems         int             `json:"totalItems"`
	CoveragePct        float64         `json:"coveragePct"`
	IsComplete         bool            `json:"isComplete"`
	TotalPrice         float64         `json:"totalPrice"`
	TotalListPrice     float64         `json:"totalListPrice"`
	SavedFromList      float64         `json:"savedFromList"`
	EstimatedFullTotal float64         `json:"estimatedFullTotal"`
	DiffFromCheapest   float64         `json:"diffFromCheapest"`
	DiffPct            float64         `json:"diffPct"`
	Lines              []StoreCartLine `json:"lines"`
}

// SplitStoreAllocation groups the products that should be bought at a specific store in an optimal split purchase.
type SplitStoreAllocation struct {
	Store      string          `json:"store"`
	BadgeColor string          `json:"badgeColor,omitempty"`
	ItemCount  int             `json:"itemCount"`
	Subtotal   float64         `json:"subtotal"`
	Lines      []StoreCartLine `json:"lines"`
}

// OptimalSplitSummary represents buying each item at the cheapest selected supermarket.
type OptimalSplitSummary struct {
	TotalPrice           float64                `json:"totalPrice"`
	CoveredItems         int                    `json:"coveredItems"`
	TotalItems           int                    `json:"totalItems"`
	BestSingleStore      string                 `json:"bestSingleStore,omitempty"`
	BestSingleStorePrice float64                `json:"bestSingleStorePrice,omitempty"`
	WorstSingleStore     string                 `json:"worstSingleStore,omitempty"`
	WorstSinglePrice     float64                `json:"worstSinglePrice,omitempty"`
	SavedVsBestSingle    float64                `json:"savedVsBestSingle"`
	SavedVsWorstSingle   float64                `json:"savedVsWorstSingle"`
	SavedVsWorstPct      float64                `json:"savedVsWorstPct"`
	Allocations          []SplitStoreAllocation `json:"allocations"`
}

// ProductMatrixRow shows a single cart product across all selected stores.
type ProductMatrixRow struct {
	GroupKey      string                   `json:"groupKey"`
	EAN           string                   `json:"ean,omitempty"`
	Title         string                   `json:"title"`
	Brand         string                   `json:"brand,omitempty"`
	Image         string                   `json:"image,omitempty"`
	Quantity      int                      `json:"quantity"`
	BestStore     string                   `json:"bestStore,omitempty"`
	BestUnitPrice float64                  `json:"bestUnitPrice,omitempty"`
	BestSubtotal  float64                  `json:"bestSubtotal,omitempty"`
	MaxSubtotal   float64                  `json:"maxSubtotal,omitempty"`
	ByStore       map[string]StoreCartLine `json:"byStore"`
}

// CartCompareResponse is returned by POST /api/cart/compare and GET /api/cart/compare.
type CartCompareResponse struct {
	TotalItems     int                `json:"totalItems"`
	TotalUnits     int                `json:"totalUnits"`
	SelectedStores []string           `json:"selectedStores"`
	StoreRanking   []StoreCartSummary `json:"storeRanking"`
	OptimalSplit   OptimalSplitSummary `json:"optimalSplit"`
	Matrix         []ProductMatrixRow `json:"matrix"`
}

