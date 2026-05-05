package printful

// BellaCanvas3001ProductID is Printful's catalog id for the Bella+Canvas
// 3001 unisex tee. The plan pins this product for V1; a UI variant picker
// is out of scope.
const BellaCanvas3001ProductID = 71

// DefaultProductName is the name written to Printful's sync_product.name.
// Printful's dashboard displays this; keep it human-friendly.
const DefaultProductName = "Thank You Bag Tee"

// DefaultPrintTechnique is "dtg" — direct-to-garment printing, the right
// technique for a single-image full-front print on a Bella+Canvas 3001.
const DefaultPrintTechnique = "dtg"

// DefaultPrintPlacement is "front" — the only placement V1 produces.
const DefaultPrintPlacement = "front"

// DefaultRetailPrice is the string Printful expects for sync_variants[].retail_price.
// User-confirmed at $30 flat across S/M/L/XL with free worldwide shipping baked
// in (TYB-12 plan, "Resolved decisions"). Stripe Checkout's unit_amount is
// derived from this value × 100; the STRIPE_PRICE_USD_CENTS env var can
// override at runtime without re-deploying.
const DefaultRetailPrice = "30.00"

// DefaultVariant is one row in DefaultVariants. Size is human-readable for
// the comment / log; VariantID is the Printful catalog variant id we send.
type DefaultVariant struct {
	Size        string
	VariantID   int
	RetailPrice string
}

// DefaultVariants is the V1 set: Bella+Canvas 3001 unisex tee, white,
// sizes S/M/L/XL.
//
// IMPORTANT: VariantID is currently 0 — a placeholder that Printful will
// reject with HTTP 422. The implementation agent could not enumerate the
// real ids without a token. The human has to either:
//
//   1. Run `curl -H "Authorization: Bearer $PRINTFUL_TOKEN" \
//        https://api.printful.com/products/71` and pick the four IDs
//        for white S/M/L/XL, OR
//   2. Visit the Printful dashboard and copy them out by hand.
//
// Update the four VariantID values below. Do not panic in init: the
// server should still boot with placeholders so the render path and
// non-Printful endpoints work in dev.
//
// RetailPrice is also a placeholder; ask the human before charging $25.
var DefaultVariants = []DefaultVariant{
	{Size: "S", VariantID: 0, RetailPrice: DefaultRetailPrice},
	{Size: "M", VariantID: 0, RetailPrice: DefaultRetailPrice},
	{Size: "L", VariantID: 0, RetailPrice: DefaultRetailPrice},
	{Size: "XL", VariantID: 0, RetailPrice: DefaultRetailPrice},
}

// DefaultVariantIDs returns just the variant ids from DefaultVariants — the
// shape mockup-task creation wants. Filters zeros so a partially-configured
// catalog still produces a sensible (if smaller) request.
func DefaultVariantIDs() []int {
	ids := make([]int, 0, len(DefaultVariants))
	for _, v := range DefaultVariants {
		if v.VariantID == 0 {
			continue
		}
		ids = append(ids, v.VariantID)
	}
	return ids
}

// CatalogConfigured reports whether the variant catalog has been populated
// with real Printful variant ids. Returns false when every entry is the
// VariantID = 0 placeholder. The /api/checkout/start handler uses this to
// 503 with a "variant catalog incomplete" message rather than calling
// Printful with a broken request — graceful degradation that mirrors how
// the routes 503 when PRINTFUL_TOKEN is missing.
func CatalogConfigured() bool {
	return len(DefaultVariantIDs()) > 0
}

// SupportedCountries lists the ISO 3166-1 alpha-2 country codes Stripe is
// allowed to surface in the Checkout shipping-address picker. Sourced from
// Printful's published list of countries they ship to (per the user's
// "worldwide" decision in TYB-12). Codes are taken from Printful's
// /countries reference at https://developers.printful.com/docs/#tag/Countries-API
// — major markets plus most jurisdictions Printful supports as of 2025.
//
// Maintenance note: revisit if Printful adds or drops a country. The
// downside of staleness is one-sided — if Printful stopped supporting a
// country listed here, the order will fail at fulfillment time and the
// human can refund manually; the worse outcome would be over-restricting
// and silently turning away customers.
var SupportedCountries = []string{
	"AD", "AE", "AG", "AL", "AM", "AR", "AT", "AU", "AZ",
	"BA", "BB", "BD", "BE", "BG", "BH", "BN", "BO", "BR", "BS", "BW", "BY", "BZ",
	"CA", "CH", "CL", "CN", "CO", "CR", "CY", "CZ",
	"DE", "DK", "DO",
	"EC", "EE", "EG", "ES", "ET",
	"FI", "FJ", "FR",
	"GB", "GE", "GH", "GR", "GT",
	"HK", "HN", "HR", "HU",
	"ID", "IE", "IL", "IN", "IS", "IT",
	"JM", "JO", "JP",
	"KE", "KG", "KH", "KR", "KW", "KZ",
	"LA", "LB", "LI", "LK", "LT", "LU", "LV",
	"MA", "MC", "MD", "ME", "MK", "MN", "MT", "MU", "MV", "MX", "MY",
	"NA", "NG", "NI", "NL", "NO", "NP", "NZ",
	"OM",
	"PA", "PE", "PH", "PK", "PL", "PT", "PY",
	"QA",
	"RO", "RS", "RW",
	"SA", "SB", "SC", "SE", "SG", "SI", "SK", "SN", "SR", "SV",
	"TH", "TN", "TR", "TT", "TW", "TZ",
	"UA", "UG", "US", "UY", "UZ",
	"VC", "VE", "VG", "VN",
	"ZA", "ZM",
}
