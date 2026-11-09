package verwaltung

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smue2012/sm-arthub/internal/schluessel"
)

type fakeSchluessel struct{ angelegt []string }

func (f *fakeSchluessel) Anlegen(_ context.Context, name string) (string, error) {
	f.angelegt = append(f.angelegt, name)
	return "ah_neu_" + name, nil
}
func (f *fakeSchluessel) Sperren(_ context.Context, name string) error {
	if name != "smerp" {
		return errors.New("fehlt")
	}
	return nil
}
func (f *fakeSchluessel) Liste(context.Context) ([]schluessel.Eintrag, error) { return nil, nil }

func neu(t *testing.T) (http.Handler, *fakeSchluessel) {
	f := &fakeSchluessel{}
	h := Handler(Optionen{
		Passwort:   "richtig-und-lang",
		Schluessel: f,
		Status:     func(context.Context) (Status, error) { return Status{Artikel: 3}, nil },
		Log:        slog.New(slog.DiscardHandler),
	})
	return h, f
}

func aufruf(h http.Handler, methode, pfad, body string, cookie *http.Cookie, mitKopf bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(methode, pfad, strings.NewReader(body))
	if mitKopf {
		req.Header.Set("X-Arthub", "1")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func anmelden(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := aufruf(h, "POST", "/api/anmelden", `{"passwort":"richtig-und-lang"}`, nil, true)
	if rec.Code != 200 {
		t.Fatalf("anmelden: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("cookie unsicher: %+v", c)
			}
			return c
		}
	}
	t.Fatal("kein Sitzungscookie")
	return nil
}

func TestAnmeldung(t *testing.T) {
	h, _ := neu(t)
	if rec := aufruf(h, "POST", "/api/anmelden", `{"passwort":"falsch"}`, nil, true); rec.Code != 401 {
		t.Fatalf("falsches Passwort: %d", rec.Code)
	}
	if rec := aufruf(h, "POST", "/api/anmelden", `{"passwort":"x","mehr":1}`, nil, true); rec.Code != 400 {
		t.Fatalf("unbekanntes Feld: %d", rec.Code)
	}
	if rec := aufruf(h, "GET", "/api/schluessel", "", nil, true); rec.Code != 401 {
		t.Fatalf("ohne Anmeldung: %d", rec.Code)
	}
	c := anmelden(t, h)
	if rec := aufruf(h, "GET", "/api/status", "", c, true); rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec := aufruf(h, "POST", "/api/abmelden", "", c, true); rec.Code != 200 {
		t.Fatalf("abmelden: %d", rec.Code)
	}
	if rec := aufruf(h, "GET", "/api/status", "", c, true); rec.Code != 401 {
		t.Fatalf("nach Abmelden: %d", rec.Code)
	}
}

func TestOhneKopfAbgelehnt(t *testing.T) {
	h, f := neu(t)
	c := anmelden(t, h)
	if rec := aufruf(h, "POST", "/api/schluessel", `{"name":"smerp"}`, c, false); rec.Code != 403 {
		t.Fatalf("ohne X-Arthub: %d", rec.Code)
	}
	if len(f.angelegt) != 0 {
		t.Fatal("trotzdem angelegt")
	}
}

func TestSchluesselAnlegen(t *testing.T) {
	h, f := neu(t)
	c := anmelden(t, h)
	rec := aufruf(h, "POST", "/api/schluessel", `{"name":"smerp"}`, c, true)
	if rec.Code != 200 {
		t.Fatalf("anlegen: %d %s", rec.Code, rec.Body)
	}
	var erg map[string]string
	json.Unmarshal(rec.Body.Bytes(), &erg)
	if erg["schluessel"] != "ah_neu_smerp" || len(f.angelegt) != 1 {
		t.Fatalf("antwort %v", erg)
	}
	for _, name := range []string{"", "SMERP", "a b", strings.Repeat("x", 41), "-x"} {
		body, _ := json.Marshal(map[string]string{"name": name})
		if rec := aufruf(h, "POST", "/api/schluessel", string(body), c, true); rec.Code != 400 {
			t.Errorf("name %q: %d", name, rec.Code)
		}
	}
	if rec := aufruf(h, "POST", "/api/schluessel/smerp/sperren", "", c, true); rec.Code != 200 {
		t.Fatalf("sperren: %d", rec.Code)
	}
	if rec := aufruf(h, "POST", "/api/schluessel/fehlt/sperren", "", c, true); rec.Code != 404 {
		t.Fatalf("sperren unbekannt: %d", rec.Code)
	}
}

func TestSeiteUndKoepfe(t *testing.T) {
	h, _ := neu(t)
	rec := aufruf(h, "GET", "/", "", nil, false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "verwaltung.js") {
		t.Fatalf("seite: %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("csp: %q", csp)
	}
	if rec := aufruf(h, "GET", "/verwaltung.js", "", nil, false); rec.Code != 200 {
		t.Fatalf("js: %d", rec.Code)
	}
}

func TestOhnePasswortKeineAnmeldung(t *testing.T) {
	h := Handler(Optionen{Schluessel: &fakeSchluessel{}, Log: slog.New(slog.DiscardHandler)})
	if rec := aufruf(h, "POST", "/api/anmelden", `{"passwort":""}`, nil, true); rec.Code != 401 {
		t.Fatalf("leeres Passwort: %d", rec.Code)
	}
}
