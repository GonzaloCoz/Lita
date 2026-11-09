package normalize

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/GonzaloCoz/Lita/internal/domain"
)

var (
	reNonDigits = regexp.MustCompile(`\D+`)
	reMeasure   = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(litros|litro|cm3|lts|grs|ml|cc|lt|kg|gr|l|g)\b`)
	rePackX     = regexp.MustCompile(`(?i)(?:pack\s*(?:x\s*)?|(?:\b|_)x\s*)(\d{1,2})\b`)
	rePackUnits = regexp.MustCompile(`(?i)\b(\d{1,2})\s*(?:unidades|uds|un|u)\b`)
)

// IsEANQuery returns true if the user search query is an 8 or 13 digit barcode.
func IsEANQuery(q string) (string, bool) {
	digits := reNonDigits.ReplaceAllString(strings.TrimSpace(q), "")
	if len(digits) == 8 || len(digits) == 13 {
		return digits, true
	}
	return "", false
}

// NormalizeEAN ports module 63264 from comprabien.app:
// - Accepts 8-digit (EAN-8) or 11..14-digit (UPC/EAN-13/GTIN-14) barcodes.
// - Rejects internal supermarket weighable scale barcodes starting with 02, 04, or 2.
func NormalizeEAN(raw string) string {
	if raw == "" {
		return ""
	}
	digits := reNonDigits.ReplaceAllString(raw, "")
	n := len(digits)
	if n != 8 && (n < 11 || n > 14) {
		return ""
	}
	trimmed := digits
	if n != 8 {
		trimmed = strings.TrimLeft(digits, "0")
	}
	if len(trimmed) < 8 {
		return ""
	}
	if n == 8 {
		if strings.HasPrefix(trimmed, "0") || strings.HasPrefix(trimmed, "2") {
			return ""
		}
		return trimmed
	}
	if len(trimmed) <= 13 {
		padded := fmt.Sprintf("%013s", trimmed)
		if strings.HasPrefix(padded, "02") || strings.HasPrefix(padded, "04") || strings.HasPrefix(padded, "2") {
			return ""
		}
	}
	return trimmed
}

// GroupKey returns the canonical grouping key for a product across stores.
func GroupKey(p domain.Product) string {
	ean := p.NormalizedEAN
	if ean == "" {
		ean = NormalizeEAN(p.EAN)
	}
	if ean != "" {
		return "ean:" + ean
	}
	return fmt.Sprintf("%s:%s", p.Supermarket, p.ID)
}

type unitDef struct {
	kind   string
	factor float64
}

var unitTable = map[string]unitDef{
	"litros": {kind: "volume", factor: 1000},
	"litro":  {kind: "volume", factor: 1000},
	"lts":    {kind: "volume", factor: 1000},
	"lt":     {kind: "volume", factor: 1000},
	"l":      {kind: "volume", factor: 1000},
	"cm3":    {kind: "volume", factor: 1},
	"ml":     {kind: "volume", factor: 1},
	"cc":     {kind: "volume", factor: 1},
	"kg":     {kind: "weight", factor: 1000},
	"grs":    {kind: "weight", factor: 1},
	"gr":     {kind: "weight", factor: 1},
	"g":      {kind: "weight", factor: 1},
}

// ParseUnitMeasure extracts volume/weight and pack count from a product title and computes $/L or $/kg.
func ParseUnitMeasure(title string, effectivePrice float64) *domain.UnitMeasure {
	matches := reMeasure.FindAllStringSubmatchIndex(title, -1)
	if len(matches) == 0 {
		return nil
	}

	var chosenKind string
	var chosenAmount float64
	var numStart, numEnd int
	found := false

	for _, m := range matches {
		numStr := strings.ReplaceAll(title[m[2]:m[3]], ",", ".")
		unitStr := strings.ToLower(title[m[4]:m[5]])
		val, err := strconv.ParseFloat(numStr, 64)
		if err != nil || val <= 0 {
			continue
		}
		def, ok := unitTable[unitStr]
		if !ok {
			continue
		}
		amount := math.Round(val*def.factor*1000) / 1000
		if def.kind == "volume" && (amount < 50 || amount > 20000) {
			continue
		}
		if def.kind == "weight" && (amount < 10 || amount > 20000) {
			continue
		}
		chosenKind = def.kind
		chosenAmount = amount
		numStart = m[2]
		numEnd = m[3]
		found = true
		break
	}

	if !found {
		return nil
	}

	pack := 1
	if pm := rePackX.FindAllStringSubmatchIndex(title, -1); len(pm) > 0 {
		for _, m := range pm {
			if m[2] == numStart && m[3] == numEnd {
				continue
			}
			if v, err := strconv.Atoi(title[m[2]:m[3]]); err == nil && v >= 2 && v <= 48 {
				pack = v
				break
			}
		}
	}
	if pack == 1 {
		if pm := rePackUnits.FindAllStringSubmatchIndex(title, -1); len(pm) > 0 {
			for _, m := range pm {
				if m[2] == numStart && m[3] == numEnd {
					continue
				}
				if v, err := strconv.Atoi(title[m[2]:m[3]]); err == nil && v >= 2 && v <= 48 {
					pack = v
					break
				}
			}
		}
	}

	totalAmount := chosenAmount * float64(pack)
	label := formatUnitLabel(chosenKind, chosenAmount, pack)

	refUnit := "kg"
	if chosenKind == "volume" {
		refUnit = "L"
	}
	var refPrice float64
	if effectivePrice > 0 && totalAmount > 0 {
		refPrice = math.Round((effectivePrice/(totalAmount/1000.0))*100) / 100
	}

	return &domain.UnitMeasure{
		Kind:           chosenKind,
		UnitAmount:     chosenAmount,
		Pack:           pack,
		TotalAmount:    totalAmount,
		Label:          label,
		ReferencePrice: refPrice,
		ReferenceUnit:  refUnit,
	}
}

func formatUnitLabel(kind string, amount float64, pack int) string {
	smallUnit, bigUnit := "g", "kg"
	if kind == "volume" {
		smallUnit, bigUnit = "ml", "L"
	}
	var base string
	if amount < 1000 {
		base = fmt.Sprintf("%s %s", formatDec(amount), smallUnit)
	} else {
		base = fmt.Sprintf("%s %s", formatDec(amount/1000.0), bigUnit)
	}
	if pack > 1 {
		return fmt.Sprintf("%s × %d", base, pack)
	}
	return base
}

func formatDec(v float64) string {
	if math.Abs(v-math.Round(v)) < 1e-6 {
		return strconv.Itoa(int(math.Round(v)))
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return strings.ReplaceAll(s, ".", ",")
}

// NormalizeText removes accents and lowercases text for fast local filtering.
func NormalizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case 'á', 'à', 'ä', 'â':
			b.WriteRune('a')
		case 'é', 'è', 'ë', 'ê':
			b.WriteRune('e')
		case 'í', 'ì', 'ï', 'î':
			b.WriteRune('i')
		case 'ó', 'ò', 'ö', 'ô':
			b.WriteRune('o')
		case 'ú', 'ù', 'ü', 'û':
			b.WriteRune('u')
		default:
			if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
				b.WriteRune(r)
			} else {
				b.WriteRune(' ')
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
