package main

import "testing"

// TestValidatePublicBaseURL covers the four-case matrix the boot guard
// enforces. The guard fails closed whenever Stripe or Printful is configured
// without an explicit PUBLIC_BASE_URL — a missing base would let a Host:
// header attacker pin Printful sync_product file URLs at their host or
// hijack Stripe's success_url. With neither upstream configured the
// renderer-only path is safe.
func TestValidatePublicBaseURL(t *testing.T) {
	cases := []struct {
		name          string
		publicBaseURL string
		stripeKey     string
		printfulToken string
		wantErr       bool
	}{
		{
			name: "render only: nothing configured, no base, allowed",
		},
		{
			name:      "stripe configured, no base, blocked",
			stripeKey: "sk_test_xxx",
			wantErr:   true,
		},
		{
			name:          "printful configured, no base, blocked",
			printfulToken: "pf_xxx",
			wantErr:       true,
		},
		{
			name:          "both configured with base, allowed",
			publicBaseURL: "https://example.com",
			stripeKey:     "sk_test_xxx",
			printfulToken: "pf_xxx",
		},
		{
			name:          "stripe with base, allowed",
			publicBaseURL: "https://example.com",
			stripeKey:     "sk_test_xxx",
		},
		{
			name:          "printful with base, allowed",
			publicBaseURL: "https://example.com",
			printfulToken: "pf_xxx",
		},
		{
			name:          "both configured no base, blocked",
			stripeKey:     "sk_test_xxx",
			printfulToken: "pf_xxx",
			wantErr:       true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePublicBaseURL(tc.publicBaseURL, tc.stripeKey, tc.printfulToken)
			if tc.wantErr && err == nil {
				t.Fatalf("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
