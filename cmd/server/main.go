package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GonzaloCoz/Lita/internal/domain"
	"github.com/GonzaloCoz/Lita/internal/engine"
	"github.com/GonzaloCoz/Lita/internal/providers"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	agg := engine.NewAggregator()
	mux := http.NewServeMux()

	// 1. Health check
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "lita-backend",
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// 2. Store Catalog
	mux.HandleFunc("GET /api/stores", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"stores": agg.Catalog(),
		})
	})

	// 3. Product & Barcode Search (GET /api/search)
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "El parámetro 'q' (búsqueda por texto o código EAN) es obligatorio",
			})
			return
		}

		var stores []string
		if rawStores := strings.TrimSpace(r.URL.Query().Get("stores")); rawStores != "" {
			for _, s := range strings.Split(rawStores, ",") {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					stores = append(stores, trimmed)
				}
			}
		}

		// Optional seller selections passed as JSON or seller_<StoreName>=<sellerId>
		selections := make(map[string]domain.BranchSelection)
		if rawSel := r.URL.Query().Get("selections"); rawSel != "" {
			_ = json.Unmarshal([]byte(rawSel), &selections)
		}
		for k, vals := range r.URL.Query() {
			if strings.HasPrefix(k, "seller_") && len(vals) > 0 && vals[0] != "" {
				stName := strings.TrimPrefix(k, "seller_")
				sellerID := vals[0]
				selections[stName] = domain.BranchSelection{
					SellerID: sellerID,
					RegionID: providers.BuildRegionID(sellerID),
				}
			}
		}

		cp := strings.TrimSpace(r.URL.Query().Get("cp"))
		sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
		if sortBy == "" {
			sortBy = "relevance"
		}
		preferDirect := r.URL.Query().Get("direct") == "1" || r.URL.Query().Get("direct") == "true"
		forceRefresh := r.URL.Query().Get("refresh") == "1" || r.URL.Query().Get("refresh") == "true"
		relevanceFilter := r.URL.Query().Get("relevance") != "0" && r.URL.Query().Get("relevance") != "false"

		ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
		defer cancel()

		res := agg.Search(ctx, engine.SearchOptions{
			Query:           q,
			Stores:          stores,
			Selections:      selections,
			PostalCode:      cp,
			RelevanceFilter: relevanceFilter,
			ForceRefresh:    forceRefresh,
			PreferDirect:    preferDirect,
			SortBy:          sortBy,
		})

		writeJSON(w, http.StatusOK, res)
	})

	// 4. Explicit EAN Barcode Search (GET /api/ean/{ean})
	mux.HandleFunc("GET /api/ean/{ean}", func(w http.ResponseWriter, r *http.Request) {
		ean := strings.TrimSpace(r.PathValue("ean"))
		if ean == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "EAN vacío"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		res := agg.Search(ctx, engine.SearchOptions{
			Query:        ean,
			ForceRefresh: r.URL.Query().Get("refresh") == "1",
			PreferDirect: r.URL.Query().Get("direct") == "1",
			SortBy:       "price_asc",
		})
		writeJSON(w, http.StatusOK, res)
	})

	// 5. Branch Seller Resolver by Postal Code or GPS (GET/POST /api/branches)
	mux.HandleFunc("/api/branches", func(w http.ResponseWriter, r *http.Request) {
		var cp string
		var lat, lng *float64

		if r.Method == http.MethodPost {
			var body struct {
				PostalCode string   `json:"postalCode"`
				Lat        *float64 `json:"lat"`
				Lng        *float64 `json:"lng"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			cp = strings.TrimSpace(body.PostalCode)
			lat = body.Lat
			lng = body.Lng
		} else {
			cp = strings.TrimSpace(r.URL.Query().Get("cp"))
			if latStr := r.URL.Query().Get("lat"); latStr != "" {
				if v, err := strconv.ParseFloat(latStr, 64); err == nil {
					lat = &v
				}
			}
			if lngStr := r.URL.Query().Get("lng"); lngStr != "" {
				if v, err := strconv.ParseFloat(lngStr, 64); err == nil {
					lng = &v
				}
			}
		}

		if cp == "" && (lat == nil || lng == nil) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "Indicá 'cp' (código postal) o coordenadas 'lat'/'lng'",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()

		res, err := agg.ResolveBranches(ctx, cp, lat, lng)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// 6. Bank & Wallet Discounts (GET /api/discounts)
	mux.HandleFunc("GET /api/discounts", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()

		res, err := agg.FetchDiscounts(ctx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// 7. Shopping Cart & Multi-Supermarket Comparison Engine
	cart := engine.NewCartManager(agg)

	mux.HandleFunc("GET /api/cart", func(w http.ResponseWriter, r *http.Request) {
		items := cart.GetItems()
		writeJSON(w, http.StatusOK, map[string]any{
			"totalItems": len(items),
			"items":      items,
		})
	})

	mux.HandleFunc("POST /api/cart/items", func(w http.ResponseWriter, r *http.Request) {
		var item domain.CartItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido: " + err.Error()})
			return
		}
		items := cart.UpsertItem(item)
		writeJSON(w, http.StatusOK, map[string]any{
			"totalItems": len(items),
			"items":      items,
		})
	})

	mux.HandleFunc("DELETE /api/cart", func(w http.ResponseWriter, r *http.Request) {
		gk := strings.TrimSpace(r.URL.Query().Get("groupKey"))
		if gk != "" {
			items := cart.RemoveItem(gk)
			writeJSON(w, http.StatusOK, map[string]any{
				"totalItems": len(items),
				"items":      items,
			})
			return
		}
		cart.Clear()
		writeJSON(w, http.StatusOK, map[string]any{
			"totalItems": 0,
			"items":      []domain.CartItem{},
		})
	})

	mux.HandleFunc("/api/cart/compare", func(w http.ResponseWriter, r *http.Request) {
		var req domain.CartCompareRequest
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido: " + err.Error()})
				return
			}
		} else {
			if rawStores := strings.TrimSpace(r.URL.Query().Get("stores")); rawStores != "" {
				for _, s := range strings.Split(rawStores, ",") {
					if trimmed := strings.TrimSpace(s); trimmed != "" {
						req.Stores = append(req.Stores, trimmed)
					}
				}
			}
			req.ResolveMissing = r.URL.Query().Get("resolve") == "1" || r.URL.Query().Get("resolve") == "true"
		}

		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()

		res := cart.Compare(ctx, req)
		writeJSON(w, http.StatusOK, res)
	})

	// 8. Shareable Supermarket Shopping Lists (POST /api/lists, GET /api/lists/{id}, POST /api/lists/{id}/check)
	mux.HandleFunc("POST /api/lists", func(w http.ResponseWriter, r *http.Request) {
		var list domain.ShoppingList
		if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido: " + err.Error()})
			return
		}
		saved := cart.SaveList(list)
		writeJSON(w, http.StatusOK, saved)
	})

	mux.HandleFunc("GET /api/lists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		list, ok := cart.GetList(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Lista no encontrada"})
			return
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("POST /api/lists/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		var body struct {
			GroupKey string `json:"groupKey"`
			Checked  bool   `json:"checked"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido"})
			return
		}
		list, ok := cart.ToggleListItemCheck(id, body.GroupKey, body.Checked)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Lista no encontrada"})
			return
		}
		writeJSON(w, http.StatusOK, list)
	})

	// 9. Serve minimalist web frontend from ./web
	webDir := filepath.Join(".", "web")
	fs := http.FileServer(http.Dir(webDir))
	mux.Handle("/", fs)

	handler := corsMiddleware(mux)

	log.Printf("🚀 Lita Backend escuchando en http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(data)
}
