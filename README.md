# Lita — Comparador Multi-Supermercado (Go Backend + Minimalist Web)

Motor de búsqueda y auditoría de precios en tiempo real para supermercados y farmacias de Argentina, desarrollado en **Go 1.24**.

## Arquitectura del Backend

```
Lita/
├── cmd/server/main.go             # Servidor HTTP REST + Frontend estático
├── internal/
│   ├── domain/                    # Modelos canónicos (Product, ProductGroup, StoreInfo, Discount)
│   ├── normalize/                 # Normalizador de EAN-8/EAN-13 y calculadora de precio por Kg/Litro
│   ├── providers/
│   │   ├── vtex.go                # Conector directo a APIs VTEX (catalog_system) con regionalización SW#<sellerId>
│   │   └── comprabien.go          # Conector de Server Actions (React Flight) y APIs de sucursales/descuentos
│   └── engine/
│       └── aggregator.go          # Orquestador concurrente (Goroutine Fan-Out / Fan-In) + Caché TTL
└── web/
    └── index.html                 # Buscador minimalista para pruebas y auditoría JSON
```

## Cómo ejecutar localmente

```bash
go run ./cmd/server
```

El servidor levanta por defecto en `http://localhost:8080`.

## Endpoints REST

| Método | Ruta | Descripción |
| :--- | :--- | :--- |
| `GET` | `/api/health` | Estado del servidor |
| `GET` | `/api/stores` | Catálogo de las 22 cadenas soportadas |
| `GET` | `/api/search?q=<texto_o_ean>` | Búsqueda concurrente en todos los supermercados (parámetros opcionales: `stores`, `cp`, `sort`, `direct=1`, `refresh=1`) |
| `GET` | `/api/ean/{ean}` | Comparación directa por código de barras EAN-8 / EAN-13 |
| `GET/POST` | `/api/branches?cp=1425` | Resolución de sucursales (`sellerId` y `regionId`) por código postal o coordenadas `lat`/`lng` |
| `GET` | `/api/discounts` | Promociones bancarias, billeteras virtuales y clubes |
