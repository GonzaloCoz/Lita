package engine

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GonzaloCoz/Lita/internal/domain"
	"github.com/GonzaloCoz/Lita/internal/normalize"
	"github.com/GonzaloCoz/Lita/internal/providers"
)

type cacheEntry struct {
	products []domain.Product
	source   string
	ts       time.Time
}

// SearchOptions configures a multi-store search execution.
type SearchOptions struct {
	Query           string
	Stores          []string // empty = all default enabled stores
	Selections      map[string]domain.BranchSelection
	PostalCode      string
	RelevanceFilter bool
	ForceRefresh    bool
	PreferDirect    bool   // if true, try direct VTEX API before CompraBien action
	SortBy          string // "relevance" | "price_asc" | "price_desc" | "unit_asc" | "savings_desc"
}

// Aggregator orchestrates concurrent product searches across all stores.
type Aggregator struct {
	catalogByID   map[string]domain.StoreInfo
	catalogByName map[string]domain.StoreInfo
	orderedStores []domain.StoreInfo
	vtex          *providers.VTEXClient
	cb            *providers.CompraBienClient

	mu       sync.RWMutex
	cache    map[string]cacheEntry
	cacheTTL time.Duration
}

func NewAggregator() *Aggregator {
	cat := domain.Catalog()
	byID := make(map[string]domain.StoreInfo, len(cat))
	byName := make(map[string]domain.StoreInfo, len(cat))
	for _, s := range cat {
		byID[strings.ToLower(s.ID)] = s
		byName[strings.ToLower(s.Name)] = s
	}
	return &Aggregator{
		catalogByID:   byID,
		catalogByName: byName,
		orderedStores: cat,
		vtex:          providers.NewVTEXClient(12 * time.Second),
		cb:            providers.NewCompraBienClient(16 * time.Second),
		cache:         make(map[string]cacheEntry),
		cacheTTL:      5 * time.Minute,
	}
}

func (a *Aggregator) Catalog() []domain.StoreInfo {
	return a.orderedStores
}

func (a *Aggregator) ResolveBranches(ctx context.Context, postalCode string, lat, lng *float64) (map[string]any, error) {
	return a.cb.ResolveBranches(ctx, postalCode, lat, lng)
}

func (a *Aggregator) FetchDiscounts(ctx context.Context) (any, error) {
	return a.cb.FetchDiscounts(ctx)
}

func (a *Aggregator) resolveTargetStores(requested []string, isEAN bool) []domain.StoreInfo {
	if len(requested) == 0 {
		out := make([]domain.StoreInfo, 0, len(a.orderedStores))
		for _, s := range a.orderedStores {
			if !s.EnabledByDef {
				continue
			}
			if isEAN && !s.SupportsEAN {
				continue
			}
			out = append(out, s)
		}
		return out
	}

	seen := make(map[string]bool)
	out := make([]domain.StoreInfo, 0, len(requested))
	for _, raw := range requested {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		s, ok := a.catalogByName[key]
		if !ok {
			s, ok = a.catalogByID[key]
		}
		if !ok || seen[s.Name] {
			continue
		}
		if isEAN && !s.SupportsEAN {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	return out
}

func (a *Aggregator) getCache(key string) ([]domain.Product, string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	entry, ok := a.cache[key]
	if !ok || time.Since(entry.ts) > a.cacheTTL {
		return nil, "", false
	}
	return entry.products, entry.source, true
}

func (a *Aggregator) setCache(key string, products []domain.Product, source string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) > 1000 {
		// Evict expired entries
		now := time.Now()
		for k, v := range a.cache {
			if now.Sub(v.ts) > a.cacheTTL {
				delete(a.cache, k)
			}
		}
	}
	a.cache[key] = cacheEntry{
		products: products,
		source:   source,
		ts:       time.Now(),
	}
}

// Search executes a concurrent fan-out search across target stores and returns grouped & individual results.
func (a *Aggregator) Search(ctx context.Context, opts SearchOptions) domain.SearchResponse {
	start := time.Now()
	q := strings.TrimSpace(opts.Query)
	eanDigits, isEAN := normalize.IsEANQuery(q)
	mode := "text"
	if isEAN {
		mode = "ean"
		q = eanDigits
	}

	targetStores := a.resolveTargetStores(opts.Stores, isEAN)
	byStore := make(map[string][]domain.Product, len(targetStores))
	timings := make([]domain.StoreTiming, 0, len(targetStores))

	var resMu sync.Mutex
	var wg sync.WaitGroup

	recordResult := func(storeName string, products []domain.Product, status string, dur time.Duration, source string, err error) {
		resMu.Lock()
		defer resMu.Unlock()
		if len(products) > 0 {
			byStore[storeName] = products
		}
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		timings = append(timings, domain.StoreTiming{
			Store:      storeName,
			Status:     status,
			Count:      len(products),
			DurationMs: dur.Milliseconds(),
			Source:     source,
			Error:      errMsg,
		})
	}

	// Cloud bundle stores (Carrefour, MasOnline, Cordiez, Disco) when in text mode and not PreferDirect
	cloudSet := map[string]bool{
		"Carrefour": true,
		"MasOnline": true,
		"Cordiez":   true,
		"Disco":     true,
	}

	var cloudStores []domain.StoreInfo
	var individualStores []domain.StoreInfo

	for _, st := range targetStores {
		sel := opts.Selections[st.Name]
		cKey := fmt.Sprintf("%s|%s|%s|%s|%v", mode, normalize.NormalizeText(q), st.Name, sel.SellerID, opts.RelevanceFilter)
		if !opts.ForceRefresh {
			if cached, src, ok := a.getCache(cKey); ok {
				status := "cached"
				if len(cached) == 0 {
					status = "empty"
				}
				recordResult(st.Name, cached, status, 0, src+" (cache)", nil)
				continue
			}
		}
		if !isEAN && !opts.PreferDirect && cloudSet[st.Name] {
			cloudStores = append(cloudStores, st)
		} else {
			individualStores = append(individualStores, st)
		}
	}

	// 1. Launch Cloud Bundle goroutine if needed
	if len(cloudStores) > 0 {
		wg.Add(1)
		go func(stores []domain.StoreInfo) {
			defer wg.Done()
			stStart := time.Now()
			bundleMap, err := a.cb.SearchCloudBundle(ctx, q, opts.Selections, opts.RelevanceFilter)
			dur := time.Since(stStart)

			for _, st := range stores {
				sel := opts.Selections[st.Name]
				cKey := fmt.Sprintf("%s|%s|%s|%s|%v", mode, normalize.NormalizeText(q), st.Name, sel.SellerID, opts.RelevanceFilter)

				if err == nil {
					prods := bundleMap[st.Name]
					a.setCache(cKey, prods, "comprabien_cloud")
					status := "ok"
					if len(prods) == 0 {
						status = "empty"
					}
					recordResult(st.Name, prods, status, dur, "comprabien_cloud", nil)
					continue
				}

				// Fallback to direct VTEX if cloud bundle failed
				fbStart := time.Now()
				prods, fbErr := a.vtex.SearchText(ctx, st, q, sel)
				fbDur := time.Since(fbStart)
				if fbErr == nil {
					a.setCache(cKey, prods, "vtex_direct")
					status := "ok"
					if len(prods) == 0 {
						status = "empty"
					}
					recordResult(st.Name, prods, status, fbDur, "vtex_direct", nil)
				} else {
					recordResult(st.Name, nil, "error", dur+fbDur, "comprabien_cloud", err)
				}
			}
		}(cloudStores)
	}

	// 2. Launch individual store goroutines
	for _, st := range individualStores {
		wg.Add(1)
		go func(store domain.StoreInfo) {
			defer wg.Done()
			stStart := time.Now()
			sel := opts.Selections[store.Name]
			cKey := fmt.Sprintf("%s|%s|%s|%s|%v", mode, normalize.NormalizeText(q), store.Name, sel.SellerID, opts.RelevanceFilter)

			var prods []domain.Product
			var src string
			var err error

			if isEAN {
				if opts.PreferDirect && store.Engine == "vtex" {
					prods, err = a.vtex.SearchEAN(ctx, store, q, sel)
					src = "vtex_direct"
					if err != nil {
						prods, err = a.cb.SearchStoreEAN(ctx, store, q, sel)
						src = "comprabien_ean"
					}
				} else {
					prods, err = a.cb.SearchStoreEAN(ctx, store, q, sel)
					src = "comprabien_ean"
					if err != nil && store.Engine == "vtex" {
						prods, err = a.vtex.SearchEAN(ctx, store, q, sel)
						src = "vtex_direct"
					}
				}
			} else {
				if opts.PreferDirect && store.Engine == "vtex" {
					prods, err = a.vtex.SearchText(ctx, store, q, sel)
					src = "vtex_direct"
					if err != nil || len(prods) == 0 {
						if cbProds, cbErr := a.cb.SearchStoreText(ctx, store, q, sel, opts.PostalCode, opts.RelevanceFilter); cbErr == nil {
							prods = cbProds
							src = "comprabien_action"
							err = nil
						}
					}
				} else {
					prods, err = a.cb.SearchStoreText(ctx, store, q, sel, opts.PostalCode, opts.RelevanceFilter)
					src = "comprabien_action"
					if err != nil && store.Engine == "vtex" {
						prods, err = a.vtex.SearchText(ctx, store, q, sel)
						src = "vtex_direct"
					}
				}
			}

			dur := time.Since(stStart)
			if err != nil {
				status := "error"
				if ctx.Err() != nil || strings.Contains(strings.ToLower(err.Error()), "timeout") {
					status = "timeout"
				}
				recordResult(store.Name, nil, status, dur, src, err)
				return
			}

			a.setCache(cKey, prods, src)
			status := "ok"
			if len(prods) == 0 {
				status = "empty"
			}
			recordResult(store.Name, prods, status, dur, src, nil)
		}(st)
	}

	wg.Wait()

	// Sort timings consistently by store order
	sort.Slice(timings, func(i, j int) bool {
		return timings[i].Store < timings[j].Store
	})

	groups, totalItems := buildProductGroups(byStore, opts.SortBy)

	return domain.SearchResponse{
		Query:        q,
		Mode:         mode,
		TotalGroups:  len(groups),
		TotalItems:   totalItems,
		DurationMs:   time.Since(start).Milliseconds(),
		Groups:       groups,
		ByStore:      byStore,
		StoreTimings: timings,
	}
}

func buildProductGroups(byStore map[string][]domain.Product, sortBy string) ([]domain.ProductGroup, int) {
	type groupAccum struct {
		key    string
		ean    string
		offers map[string]domain.Product // deduplicated by store
		order  int
	}

	accumMap := make(map[string]*groupAccum)
	var accumList []*groupAccum
	totalItems := 0
	seq := 0

	// Process stores in deterministic order
	storeNames := make([]string, 0, len(byStore))
	for k := range byStore {
		storeNames = append(storeNames, k)
	}
	sort.Strings(storeNames)

	for _, store := range storeNames {
		prods := byStore[store]
		totalItems += len(prods)

		for _, p := range prods {
			if strings.TrimSpace(p.Image) == "" || (p.Price <= 0 && !p.IsAvailable) {
				continue
			}
			gKey := normalize.GroupKey(p)
			acc, exists := accumMap[gKey]
			if !exists {
				acc = &groupAccum{
					key:    gKey,
					ean:    p.NormalizedEAN,
					offers: make(map[string]domain.Product),
					order:  seq,
				}
				seq++
				accumMap[gKey] = acc
				accumList = append(accumList, acc)
			}

			// If store already has a product for this EAN, keep the available & cheaper one
			if prev, hasPrev := acc.offers[store]; hasPrev {
				if !prev.IsAvailable && p.IsAvailable {
					acc.offers[store] = p
				} else if p.IsAvailable == prev.IsAvailable && p.Price > 0 && p.Price < prev.Price {
					acc.offers[store] = p
				}
			} else {
				acc.offers[store] = p
			}
		}
	}

	groups := make([]domain.ProductGroup, 0, len(accumList))
	for _, acc := range accumList {
		offers := make([]domain.StoreOffer, 0, len(acc.offers))
		for st, p := range acc.offers {
			offers = append(offers, domain.StoreOffer{
				Store:       st,
				Price:       p.Price,
				ListPrice:   p.ListPrice,
				IsAvailable: p.IsAvailable,
				Product:     p,
			})
		}

		// Sort offers: available first, then lowest price
		sort.Slice(offers, func(i, j int) bool {
			if offers[i].IsAvailable != offers[j].IsAvailable {
				return offers[i].IsAvailable
			}
			if offers[i].Price != offers[j].Price {
				return offers[i].Price < offers[j].Price
			}
			return offers[i].Store < offers[j].Store
		})

		anchor := offers[0]
		bestPrice := anchor.Price
		maxPrice := anchor.Price
		sumPrice := 0.0
		availCount := 0

		for _, o := range offers {
			if o.IsAvailable && o.Price > 0 {
				if o.Price > maxPrice {
					maxPrice = o.Price
				}
				sumPrice += o.Price
				availCount++
			}
		}

		avgPrice := bestPrice
		if availCount > 0 {
			avgPrice = math.Round((sumPrice/float64(availCount))*100) / 100
		}

		for i := range offers {
			if bestPrice > 0 && offers[i].Price > bestPrice {
				offers[i].DiffPct = math.Round(((offers[i].Price-bestPrice)/bestPrice)*1000) / 10
			}
		}

		maxSavingPct := 0.0
		if maxPrice > bestPrice && maxPrice > 0 {
			maxSavingPct = math.Round(((maxPrice-bestPrice)/maxPrice)*1000) / 10
		}

		// Pick longest descriptive title and brand/unitMeasure across members
		bestTitle := anchor.Product.Name
		bestBrand := anchor.Product.Brand
		bestImage := anchor.Product.Image
		var bestUnit *domain.UnitMeasure

		for _, o := range offers {
			if len(o.Product.Name) > len(bestTitle) {
				bestTitle = o.Product.Name
			}
			if bestBrand == "" && o.Product.Brand != "" {
				bestBrand = o.Product.Brand
			}
			if bestImage == "" && o.Product.Image != "" {
				bestImage = o.Product.Image
			}
			if bestUnit == nil && o.Product.UnitMeasure != nil {
				bestUnit = normalize.ParseUnitMeasure(o.Product.Name, bestPrice)
			}
		}
		if bestUnit == nil {
			bestUnit = normalize.ParseUnitMeasure(bestTitle, bestPrice)
		}

		groups = append(groups, domain.ProductGroup{
			GroupKey:     acc.key,
			EAN:          acc.ean,
			Title:        bestTitle,
			Brand:        bestBrand,
			Image:        bestImage,
			UnitMeasure:  bestUnit,
			BestPrice:    bestPrice,
			BestStore:    anchor.Store,
			MaxPrice:     maxPrice,
			AvgPrice:     avgPrice,
			MaxSavingPct: maxSavingPct,
			StoreCount:   len(offers),
			Offers:       offers,
		})
	}

	sortProductGroups(groups, sortBy)
	return groups, totalItems
}

func sortProductGroups(groups []domain.ProductGroup, sortBy string) {
	switch sortBy {
	case "price_asc":
		sort.SliceStable(groups, func(i, j int) bool {
			return groups[i].BestPrice < groups[j].BestPrice
		})
	case "price_desc":
		sort.SliceStable(groups, func(i, j int) bool {
			return groups[i].BestPrice > groups[j].BestPrice
		})
	case "unit_asc":
		sort.SliceStable(groups, func(i, j int) bool {
			ui, uj := groups[i].UnitMeasure, groups[j].UnitMeasure
			if ui != nil && uj != nil && ui.ReferencePrice > 0 && uj.ReferencePrice > 0 {
				return ui.ReferencePrice < uj.ReferencePrice
			}
			if ui != nil && ui.ReferencePrice > 0 {
				return true
			}
			if uj != nil && uj.ReferencePrice > 0 {
				return false
			}
			return groups[i].BestPrice < groups[j].BestPrice
		})
	case "savings_desc":
		sort.SliceStable(groups, func(i, j int) bool {
			if groups[i].StoreCount != groups[j].StoreCount {
				if (groups[i].StoreCount > 1) != (groups[j].StoreCount > 1) {
					return groups[i].StoreCount > 1
				}
			}
			if groups[i].MaxSavingPct != groups[j].MaxSavingPct {
				return groups[i].MaxSavingPct > groups[j].MaxSavingPct
			}
			return groups[i].StoreCount > groups[j].StoreCount
		})
	default: // "relevance": multi-store matches first, then highest relevance/proximity score, then lowest price
		sort.SliceStable(groups, func(i, j int) bool {
			multiI := groups[i].StoreCount > 1
			multiJ := groups[j].StoreCount > 1
			if multiI != multiJ {
				return multiI
			}
			if groups[i].StoreCount != groups[j].StoreCount {
				return groups[i].StoreCount > groups[j].StoreCount
			}
			return groups[i].BestPrice < groups[j].BestPrice
		})
	}
}
