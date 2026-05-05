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
// 25.00 USD is a placeholder; flag it as the human-confirmable knob.
const DefaultRetailPrice = "25.00"

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
