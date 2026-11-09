package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/smue2012/sm-arthub/internal/katalog"
	"github.com/smue2012/sm-arthub/internal/schluessel"
)

type fakeKatalog struct {
	seit, limit   int64
	artikel       []katalog.Artikel
	mehr          bool
	q             string
	nurVerfuegbar bool
}

func (f *fakeKatalog) Seit(_ context.Context, _ string, seit int64, limit int) ([]katalog.Artikel, bool, error) {
	f.seit, f.limit = seit, int64(limit)
	return f.artikel, f.mehr, nil
}

func (f *fakeKatalog) Nummer(_ context.Context, _, nummer string) (katalog.Artikel, error) {
	for _, a := range f.artikel {
		if a.Nummer == nummer {
			return a, nil
		}
	}
	return katalog.Artikel{}, katalog.ErrNichtGefunden
}

func (f *fakeKatalog) Suche(_ context.Context, _, q string, limit int, nurVerfuegbar bool) ([]katalog.Artikel, bool, error) {
	f.q, f.nurVerfuegbar, f.limit = q, nurVerfuegbar, int64(limit)
	return f.artikel, f.mehr, nil
}

func (f *fakeKatalog) Lieferanten(context.Context) ([]katalog.Lieferant, error) {
	return []katalog.Lieferant{{Kuerzel: "alltron", Name: "Alltron", Aktiv: true}}, nil
}

type fakePruefer struct{}

func (fakePruefer) Pruefen(_ context.Context, s string) (string, error) {
	if s == "ah_gut" {
		return "smerp", nil
	}
	return "", schluessel.ErrUngueltig
}

func aufruf(t *testing.T, h http.Handler, pfad, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", pfad, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	b, _ := io.ReadAll(rec.Body)
	json.Unmarshal(b, &body)
	return rec.Code, body
}

func TestSchluesselPflicht(t *testing.T) {
	h := Handler(&fakeKatalog{}, fakePruefer{}, slog.New(slog.DiscardHandler))
	if code, _ := aufruf(t, h, "/api/v1/lieferanten", ""); code != 401 {
		t.Fatalf("ohne Schlüssel: %d, erwartet 401", code)
	}
	if code, _ := aufruf(t, h, "/api/v1/lieferanten", "ah_falsch"); code != 401 {
		t.Fatalf("falscher Schlüssel: %d, erwartet 401", code)
	}
	if code, _ := aufruf(t, h, "/api/v1/lieferanten", "ah_gut"); code != 200 {
		t.Fatalf("guter Schlüssel: %d, erwartet 200", code)
	}
	if code, _ := aufruf(t, h, "/health", ""); code != 200 {
		t.Fatalf("health: %d, erwartet 200", code)
	}
}

func TestArtikelSeit(t *testing.T) {
	k := &fakeKatalog{artikel: []katalog.Artikel{{Nummer: "AH0000001", Stand: 7}, {Nummer: "AH0000002", Stand: 9}}, mehr: true}
	h := Handler(k, fakePruefer{}, slog.New(slog.DiscardHandler))

	code, body := aufruf(t, h, "/api/v1/artikel?seit=5&limit=2", "ah_gut")
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if k.seit != 5 || k.limit != 2 {
		t.Fatalf("weitergegeben seit=%d limit=%d", k.seit, k.limit)
	}
	if body["stand"].(float64) != 9 || body["mehr"] != true {
		t.Fatalf("antwort %v", body)
	}

	for _, pfad := range []string{"/api/v1/artikel?seit=-1", "/api/v1/artikel?limit=0",
		"/api/v1/artikel?limit=99999", "/api/v1/artikel?suche=x"} {
		if code, _ := aufruf(t, h, pfad, "ah_gut"); code != 400 {
			t.Errorf("%s: %d, erwartet 400", pfad, code)
		}
	}
}

func TestArtikelNummer(t *testing.T) {
	k := &fakeKatalog{artikel: []katalog.Artikel{{Nummer: "AH0000001"}}}
	h := Handler(k, fakePruefer{}, slog.New(slog.DiscardHandler))
	if code, _ := aufruf(t, h, "/api/v1/artikel/AH0000001", "ah_gut"); code != 200 {
		t.Fatalf("vorhanden: %d", code)
	}
	if code, _ := aufruf(t, h, "/api/v1/artikel/AH9999999", "ah_gut"); code != 404 {
		t.Fatalf("fehlt: %d", code)
	}
}

func TestSuche(t *testing.T) {
	k := &fakeKatalog{artikel: []katalog.Artikel{{Nummer: "AH0000001"}}}
	h := Handler(k, fakePruefer{}, slog.New(slog.DiscardHandler))
	code, body := aufruf(t, h, "/api/v1/suche?q=+logitech+maus+&verfuegbar=1&limit=10", "ah_gut")
	if code != 200 || k.q != "logitech maus" || !k.nurVerfuegbar || k.limit != 10 {
		t.Fatalf("code %d q=%q verf=%v limit=%d", code, k.q, k.nurVerfuegbar, k.limit)
	}
	if len(body["artikel"].([]any)) != 1 {
		t.Fatalf("antwort %v", body)
	}
	for _, pfad := range []string{"/api/v1/suche", "/api/v1/suche?q=a", "/api/v1/suche?q=ab&limit=500",
		"/api/v1/suche?q=ab&verfuegbar=ja", "/api/v1/suche?q=ab&x=1"} {
		if code, _ := aufruf(t, h, pfad, "ah_gut"); code != 400 {
			t.Errorf("%s: %d, erwartet 400", pfad, code)
		}
	}
	if code, _ := aufruf(t, h, "/api/v1/suche?q=ab", ""); code != 401 {
		t.Errorf("ohne Schlüssel: %d", code)
	}
}
