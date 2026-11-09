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
)

// CartManager holds an in-memory shopping cart, saved shopping lists, and runs multi-supermarket price comparisons.
type CartManager struct {
	mu    sync.RWMutex
	items map[string]domain.CartItem
	order []string
	lists map[string]domain.ShoppingList
	agg   *Aggregator
}

func NewCartManager(agg *Aggregator) *CartManager {
	return &CartManager{
		items: make(map[string]domain.CartItem),
		lists: make(map[string]domain.ShoppingList),
		agg:   agg,
	}
}

func (cm *CartManager) SaveList(list domain.ShoppingList) domain.ShoppingList {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if strings.TrimSpace(list.ID) == "" {
		list.ID = fmt.Sprintf("lst-%d", time.Now().UnixNano()/1e6)
	}
	if list.CreatedAt == "" {
		list.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	var total float64
	totalUnits := 0
	for i := range list.Items {
		if list.Items[i].Quantity <= 0 {
			list.Items[i].Quantity = 1
		}
		totalUnits += list.Items[i].Quantity
		list.Items[i].Subtotal = round2(list.Items[i].UnitPrice * float64(list.Items[i].Quantity))
		if list.Items[i].Available {
			total = round2(total + list.Items[i].Subtotal)
		}
	}
	list.TotalItems = len(list.Items)
	list.TotalUnits = totalUnits
	list.TotalPrice = total
	cm.lists[list.ID] = list
	return list
}

func (cm *CartManager) GetList(id string) (domain.ShoppingList, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	l, ok := cm.lists[strings.TrimSpace(id)]
	return l, ok
}

func (cm *CartManager) ToggleListItemCheck(listID, groupKey string, checked bool) (domain.ShoppingList, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	l, ok := cm.lists[strings.TrimSpace(listID)]
	if !ok {
		return domain.ShoppingList{}, false
	}
	for i := range l.Items {
		if l.Items[i].GroupKey == groupKey {
			l.Items[i].Checked = checked
		}
	}
	cm.lists[l.ID] = l
	return l, true
}

func (cm *CartManager) GetItems() []domain.CartItem {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	out := make([]domain.CartItem, 0, len(cm.order))
	for _, k := range cm.order {
		if it, ok := cm.items[k]; ok {
			out = append(out, it)
		}
	}
	return out
}

func (cm *CartManager) UpsertItem(item domain.CartItem) []domain.CartItem {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	key := strings.TrimSpace(item.GroupKey)
	if key == "" {
		if item.EAN != "" {
			key = "ean:" + normalize.NormalizeEAN(item.EAN)
		} else {
			key = normalize.NormalizeText(item.Title)
		}
		item.GroupKey = key
	}

	if item.Quantity <= 0 {
		delete(cm.items, key)
		newOrder := make([]string, 0, len(cm.order))
		for _, k := range cm.order {
			if k != key {
				newOrder = append(newOrder, k)
			}
		}
		cm.order = newOrder
	} else {
		if _, exists := cm.items[key]; !exists {
			cm.order = append(cm.order, key)
		}
		cm.items[key] = item
	}

	out := make([]domain.CartItem, 0, len(cm.order))
	for _, k := range cm.order {
		if it, ok := cm.items[k]; ok {
			out = append(out, it)
		}
	}
	return out
}

func (cm *CartManager) RemoveItem(groupKey string) []domain.CartItem {
	return cm.UpsertItem(domain.CartItem{GroupKey: groupKey, Quantity: 0})
}

func (cm *CartManager) Clear() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.items = make(map[string]domain.CartItem)
	cm.order = nil
}

// Compare evaluates the cost of buying the cart across the user's selected supermarkets.
func (cm *CartManager) Compare(ctx context.Context, req domain.CartCompareRequest) domain.CartCompareResponse {
	items := req.Items
	if len(items) == 0 {
		items = cm.GetItems()
	}

	targetStoreInfos := cm.agg.resolveTargetStores(req.Stores, false)
	selectedStores := make([]string, 0, len(targetStoreInfos))
	badgeColors := make(map[string]string, len(targetStoreInfos))
	selectedSet := make(map[string]bool, len(targetStoreInfos))
	for _, st := range targetStoreInfos {
		selectedStores = append(selectedStores, st.Name)
		badgeColors[st.Name] = st.BadgeColor
		selectedSet[st.Name] = true
	}

	if len(items) == 0 {
		return domain.CartCompareResponse{
			SelectedStores: selectedStores,
			StoreRanking:   []domain.StoreCartSummary{},
			Matrix:         []domain.ProductMatrixRow{},
		}
	}

	// Optionally resolve missing store offers by EAN / Title via Aggregator
	resolvedItems := make([]domain.CartItem, len(items))
	copy(resolvedItems, items)
	if req.ResolveMissing {
		for idx := range resolvedItems {
			it := &resolvedItems[idx]
			knownByStore := make(map[string]bool)
			for _, off := range it.Offers {
				knownByStore[off.Store] = true
			}
			var missingStores []string
			for _, stName := range selectedStores {
				if !knownByStore[stName] {
					missingStores = append(missingStores, stName)
				}
			}
			if len(missingStores) > 0 && it.EAN != "" {
				sr := cm.agg.Search(ctx, SearchOptions{
					Query:  it.EAN,
					Stores: missingStores,
				})
				for _, grp := range sr.Groups {
					for _, off := range grp.Offers {
						if !knownByStore[off.Store] {
							it.Offers = append(it.Offers, off)
							knownByStore[off.Store] = true
						}
					}
				}
			}
		}
	}

	totalItems := len(resolvedItems)
	totalUnits := 0

	matrix := make([]domain.ProductMatrixRow, 0, totalItems)
	// Map store -> list of StoreCartLine
	storeLinesMap := make(map[string][]domain.StoreCartLine, len(selectedStores))
	for _, st := range selectedStores {
		storeLinesMap[st] = make([]domain.StoreCartLine, 0, totalItems)
	}

	// Optimal split tracking
	splitAllocMap := make(map[string]*domain.SplitStoreAllocation)
	var optimalTotal float64
	coveredItems := 0
	rowAvgSubtotal := make([]float64, totalItems)

	for rowIdx, it := range resolvedItems {
		qty := it.Quantity
		if qty <= 0 {
			qty = 1
		}
		totalUnits += qty

		// Index offers by canonical store name (keep cheapest offer per store)
		offerByStore := make(map[string]domain.StoreOffer)
		for _, off := range it.Offers {
			if !selectedSet[off.Store] || off.Price <= 0 {
				continue
			}
			prev, exists := offerByStore[off.Store]
			if !exists || off.Price < prev.Price {
				offerByStore[off.Store] = off
			}
		}

		var bestStore string
		var bestUnitPrice float64
		var bestSubtotal float64
		var maxSubtotal float64
		var sumSubtotal float64
		availStoresForRow := 0

		for _, st := range selectedStores {
			off, ok := offerByStore[st]
			if !ok {
				continue
			}
			sub := round2(off.Price * float64(qty))
			sumSubtotal += sub
			availStoresForRow++
			if bestStore == "" || off.Price < bestUnitPrice {
				bestStore = st
				bestUnitPrice = off.Price
				bestSubtotal = sub
			}
			if sub > maxSubtotal {
				maxSubtotal = sub
			}
		}

		if availStoresForRow > 0 {
			rowAvgSubtotal[rowIdx] = round2(sumSubtotal / float64(availStoresForRow))
			coveredItems++
			optimalTotal = round2(optimalTotal + bestSubtotal)
		}

		byStoreRow := make(map[string]domain.StoreCartLine, len(selectedStores))
		for _, st := range selectedStores {
			off, ok := offerByStore[st]
			if !ok {
				line := domain.StoreCartLine{
					GroupKey:  it.GroupKey,
					EAN:       it.EAN,
					Title:     it.Title,
					Quantity:  qty,
					Available: false,
				}
				byStoreRow[st] = line
				storeLinesMap[st] = append(storeLinesMap[st], line)
				continue
			}
			lp := off.ListPrice
			if lp < off.Price {
				lp = off.Price
			}
			sub := round2(off.Price * float64(qty))
			listSub := round2(lp * float64(qty))
			isCheapest := bestUnitPrice > 0 && math.Abs(off.Price-bestUnitPrice) < 0.01

			line := domain.StoreCartLine{
				GroupKey:         it.GroupKey,
				EAN:              it.EAN,
				Title:            it.Title,
				Quantity:         qty,
				Available:        true,
				UnitPrice:        round2(off.Price),
				ListPrice:        round2(lp),
				Subtotal:         sub,
				ListSubtotal:     listSub,
				Link:             off.Product.Link,
				Image:            it.Image,
				IsCheapestForRow: isCheapest,
			}
			byStoreRow[st] = line
			storeLinesMap[st] = append(storeLinesMap[st], line)
		}

		// Record into optimal split allocation for bestStore
		if bestStore != "" {
			alloc, exists := splitAllocMap[bestStore]
			if !exists {
				alloc = &domain.SplitStoreAllocation{
					Store:      bestStore,
					BadgeColor: badgeColors[bestStore],
				}
				splitAllocMap[bestStore] = alloc
			}
			alloc.ItemCount++
			alloc.Subtotal = round2(alloc.Subtotal + bestSubtotal)
			alloc.Lines = append(alloc.Lines, byStoreRow[bestStore])
		}

		matrix = append(matrix, domain.ProductMatrixRow{
			GroupKey:      it.GroupKey,
			EAN:           it.EAN,
			Title:         it.Title,
			Brand:         it.Brand,
			Image:         it.Image,
			Quantity:      qty,
			BestStore:     bestStore,
			BestUnitPrice: round2(bestUnitPrice),
			BestSubtotal:  bestSubtotal,
			MaxSubtotal:   maxSubtotal,
			ByStore:       byStoreRow,
		})
	}

	// Build StoreRanking
	ranking := make([]domain.StoreCartSummary, 0, len(selectedStores))
	for _, st := range selectedStores {
		lines := storeLinesMap[st]
		availCount := 0
		var totalPrice float64
		var totalListPrice float64
		var estimatedFull float64

		for idx, ln := range lines {
			if ln.Available {
				availCount++
				totalPrice = round2(totalPrice + ln.Subtotal)
				totalListPrice = round2(totalListPrice + ln.ListSubtotal)
				estimatedFull = round2(estimatedFull + ln.Subtotal)
			} else {
				estimatedFull = round2(estimatedFull + rowAvgSubtotal[idx])
			}
		}

		if availCount == 0 {
			continue
		}

		missingCount := totalItems - availCount
		covPct := round1((float64(availCount) / float64(totalItems)) * 100)
		savedFromList := round2(math.Max(0, totalListPrice-totalPrice))

		ranking = append(ranking, domain.StoreCartSummary{
			Store:              st,
			BadgeColor:         badgeColors[st],
			AvailableCount:     availCount,
			MissingCount:       missingCount,
			TotalItems:         totalItems,
			CoveragePct:        covPct,
			IsComplete:         availCount == totalItems,
			TotalPrice:         totalPrice,
			TotalListPrice:     totalListPrice,
			SavedFromList:      savedFromList,
			EstimatedFullTotal: estimatedFull,
			Lines:              lines,
		})
	}

	// Sort stores: highest AvailableCount first, then lowest TotalPrice
	sort.Slice(ranking, func(i, j int) bool {
		if ranking[i].AvailableCount != ranking[j].AvailableCount {
			return ranking[i].AvailableCount > ranking[j].AvailableCount
		}
		return ranking[i].TotalPrice < ranking[j].TotalPrice
	})

	// Compute differences vs top ranked store (or cheapest store at same coverage)
	var basePrice float64
	var bestSingleStore string
	var bestSinglePrice float64
	var worstSingleStore string
	var worstSinglePrice float64

	if len(ranking) > 0 {
		maxAvail := ranking[0].AvailableCount
		basePrice = ranking[0].TotalPrice
		bestSingleStore = ranking[0].Store
		bestSinglePrice = ranking[0].TotalPrice

		for i := range ranking {
			if ranking[i].AvailableCount == maxAvail && ranking[i].TotalPrice > worstSinglePrice {
				worstSinglePrice = ranking[i].TotalPrice
				worstSingleStore = ranking[i].Store
			}
			// Compare using TotalPrice when coverage matches, or EstimatedFullTotal otherwise
			refBase := basePrice
			targetVal := ranking[i].TotalPrice
			if ranking[i].AvailableCount < maxAvail {
				refBase = ranking[0].EstimatedFullTotal
				targetVal = ranking[i].EstimatedFullTotal
			}
			if targetVal > refBase && refBase > 0 {
				ranking[i].DiffFromCheapest = round2(targetVal - refBase)
				ranking[i].DiffPct = round1(((targetVal - refBase) / refBase) * 100)
			}
		}
	}

	allocations := make([]domain.SplitStoreAllocation, 0, len(splitAllocMap))
	for _, a := range splitAllocMap {
		allocations = append(allocations, *a)
	}
	sort.Slice(allocations, func(i, j int) bool {
		if allocations[i].ItemCount != allocations[j].ItemCount {
			return allocations[i].ItemCount > allocations[j].ItemCount
		}
		return allocations[i].Subtotal < allocations[j].Subtotal
	})

	savedVsBest := 0.0
	if bestSinglePrice > optimalTotal && len(ranking) > 0 && ranking[0].AvailableCount == coveredItems {
		savedVsBest = round2(bestSinglePrice - optimalTotal)
	}
	savedVsWorst := 0.0
	savedVsWorstPct := 0.0
	if worstSinglePrice > optimalTotal && len(ranking) > 0 && ranking[0].AvailableCount == coveredItems {
		savedVsWorst = round2(worstSinglePrice - optimalTotal)
		savedVsWorstPct = round1((savedVsWorst / worstSinglePrice) * 100)
	}

	return domain.CartCompareResponse{
		TotalItems:     totalItems,
		TotalUnits:     totalUnits,
		SelectedStores: selectedStores,
		StoreRanking:   ranking,
		OptimalSplit: domain.OptimalSplitSummary{
			TotalPrice:           optimalTotal,
			CoveredItems:         coveredItems,
			TotalItems:           totalItems,
			BestSingleStore:      bestSingleStore,
			BestSingleStorePrice: bestSinglePrice,
			WorstSingleStore:     worstSingleStore,
			WorstSinglePrice:     worstSinglePrice,
			SavedVsBestSingle:    savedVsBest,
			SavedVsWorstSingle:   savedVsWorst,
			SavedVsWorstPct:      savedVsWorstPct,
			Allocations:          allocations,
		},
		Matrix: matrix,
	}
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

