package ocpi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tollgate-auth/internal/auth"
)

func TestAboutRoute(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:    ":0",
		PublicBaseURL: "http://localhost",
		OurCountry:    "NO",
		OurParty:      "TGA",
	}, &auth.Dependencies{})

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	cases := []struct {
		path       string
		wantStatus int
		wantSubstr []string
	}{
		{
			path:       "/about",
			wantStatus: 200,
			wantSubstr: []string{
				"Cashu-Gated",
				"EV Charging",
				"How it works",
				"Buy credit",
				"Get Cashu token",
				"Plug in",
				"Charger starts",
				"Architecture",
				"BTC Cashu",
				"EUR Gift Card",
				"https://ocpi.nodns.shop/ocpi/versions",
				`href="/"`,
				"NO/TGA",
			},
		},
		{path: "/", wantStatus: 200, wantSubstr: nil},
		{path: "/healthz", wantStatus: 200, wantSubstr: nil},
		{path: "/this-does-not-exist", wantStatus: 404, wantSubstr: nil},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s: status = %d, want %d", tc.path, rec.Code, tc.wantStatus)
			}
			if tc.wantSubstr == nil {
				return
			}
			body := rec.Body.String()
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s: Content-Type = %q, want text/html*", tc.path, ct)
			}
			for _, sub := range tc.wantSubstr {
				if !strings.Contains(body, sub) {
					t.Errorf("GET %s: body missing %q", tc.path, sub)
				}
			}
		})
	}
}
