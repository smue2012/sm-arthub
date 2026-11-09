// Paket verwaltung ist die Weboberfläche des Hubs: anmelden mit dem
// Verwaltungspasswort, API-Schlüssel der Applikationen anlegen, ersetzen und
// sperren, Stand des Katalogs ansehen.
//
// Sie läuft auf einem eigenen Port (ADMIN_PORT), getrennt von der Lese-API,
// damit nur die Verwaltung über den Reverse-Proxy erreichbar gemacht werden
// muss. Ein neuer Schlüssel wird genau einmal an den Browser gegeben (zum
// Kopieren in die Konfiguration der App); gespeichert ist nur sein Hash.
package verwaltung

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/smue2012/sm-arthub/internal/schluessel"
)

//go:embed static
var statisch embed.FS

// Sitzungsdauer ohne Aktivität.
const Sitzungsdauer = 8 * time.Hour

const cookieName = "arthub_sitzung"

// Schluessel: was die Oberfläche von der Schlüsselverwaltung braucht.
type Schluessel interface {
	Anlegen(ctx context.Context, name string) (string, error)
	Sperren(ctx context.Context, name string) error
	Liste(ctx context.Context) ([]schluessel.Eintrag, error)
}

// Status des Katalogs für die Kopfzeile.
type Status struct {
	Artikel     int    `json:"artikel"`
	Angebote    int    `json:"angebote"`
	Lieferanten int    `json:"lieferanten"`
	Version     string `json:"version"`
	Migration   string `json:"migration"`
}

// StatusQuelle liefert den Status (im Test eine Attrappe).
type StatusQuelle func(ctx context.Context) (Status, error)

// Optionen der Oberfläche.
type Optionen struct {
	Passwort   string
	Schluessel Schluessel
	Status     StatusQuelle
	Log        *slog.Logger
	// FehlVerzoegerung bremst falsche Anmeldungen (Vorgabe 1 s, im Test 0).
	FehlVerzoegerung time.Duration
}

var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

type server struct {
	opt       Optionen
	mu        sync.Mutex
	sitzungen map[string]time.Time // Token → letzte Aktivität
}

// Handler baut die Oberfläche.
func Handler(opt Optionen) http.Handler {
	if opt.FehlVerzoegerung < 0 {
		opt.FehlVerzoegerung = 0
	}
	s := &server{opt: opt, sitzungen: map[string]time.Time{}}

	dateien, _ := fs.Sub(statisch, "static")
	api := http.NewServeMux()
	api.HandleFunc("POST /api/anmelden", s.anmelden)
	api.HandleFunc("POST /api/abmelden", s.abmelden)
	api.Handle("GET /api/ich", s.nurAngemeldet(func(w http.ResponseWriter, r *http.Request) {
		jsonAntwort(w, http.StatusOK, map[string]bool{"angemeldet": true})
	}))
	api.Handle("GET /api/status", s.nurAngemeldet(s.status))
	api.Handle("GET /api/schluessel", s.nurAngemeldet(s.liste))
	api.Handle("POST /api/schluessel", s.nurAngemeldet(s.anlegen))
	api.Handle("POST /api/schluessel/{name}/sperren", s.nurAngemeldet(s.sperren))

	mux := http.NewServeMux()
	mux.Handle("/api/", nurMitKopf(api))
	mux.Handle("/", http.FileServerFS(dateien))
	return sicherheitsKoepfe(mux)
}

// sicherheitsKoepfe: strenge CSP (kein Inline-Skript, keine Inline-Stile),
// nicht einbettbar, nichts zwischenspeichern.
func sicherheitsKoepfe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// nurMitKopf verlangt «X-Arthub: 1» auf allen API-Aufrufen. Fremde Seiten
// können diesen Kopf nicht setzen (zusammen mit SameSite=Strict gegen CSRF).
func nurMitKopf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Arthub") != "1" {
			fehler(w, http.StatusForbidden, "Kopf X-Arthub fehlt")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) nurAngemeldet(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || !s.sitzungGueltig(c.Value) {
			fehler(w, http.StatusUnauthorized, "nicht angemeldet")
			return
		}
		next(w, r)
	})
}

func (s *server) sitzungGueltig(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	zuletzt, ok := s.sitzungen[token]
	if !ok {
		return false
	}
	if time.Since(zuletzt) > Sitzungsdauer {
		delete(s.sitzungen, token)
		return false
	}
	s.sitzungen[token] = time.Now()
	return true
}

func lesen(r *http.Request, ziel any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	return dec.Decode(ziel)
}

func istHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *server) anmelden(w http.ResponseWriter, r *http.Request) {
	var ein struct {
		Passwort string `json:"passwort"`
	}
	if err := lesen(r, &ein); err != nil {
		fehler(w, http.StatusBadRequest, "ungültige Anfrage")
		return
	}
	if s.opt.Passwort == "" ||
		subtle.ConstantTimeCompare([]byte(ein.Passwort), []byte(s.opt.Passwort)) != 1 {
		s.opt.Log.Warn("anmeldung fehlgeschlagen", "von", r.RemoteAddr)
		time.Sleep(s.opt.FehlVerzoegerung)
		fehler(w, http.StatusUnauthorized, "Passwort falsch")
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		fehler(w, http.StatusInternalServerError, "interner Fehler")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	for t, zuletzt := range s.sitzungen { // abgelaufene aufräumen
		if time.Since(zuletzt) > Sitzungsdauer {
			delete(s.sitzungen, t)
		}
	}
	s.sitzungen[token] = time.Now()
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: istHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
	s.opt.Log.Info("angemeldet", "von", r.RemoteAddr)
	jsonAntwort(w, http.StatusOK, map[string]bool{"angemeldet": true})
}

func (s *server) abmelden(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sitzungen, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: istHTTPS(r), SameSite: http.SameSiteStrictMode})
	jsonAntwort(w, http.StatusOK, map[string]bool{"angemeldet": false})
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.opt.Status(r.Context())
	if err != nil {
		s.serverfehler(w, err)
		return
	}
	jsonAntwort(w, http.StatusOK, st)
}

func (s *server) liste(w http.ResponseWriter, r *http.Request) {
	liste, err := s.opt.Schluessel.Liste(r.Context())
	if err != nil {
		s.serverfehler(w, err)
		return
	}
	if liste == nil {
		liste = []schluessel.Eintrag{}
	}
	jsonAntwort(w, http.StatusOK, map[string]any{"schluessel": liste})
}

func (s *server) anlegen(w http.ResponseWriter, r *http.Request) {
	var ein struct {
		Name string `json:"name"`
	}
	if err := lesen(r, &ein); err != nil {
		fehler(w, http.StatusBadRequest, "ungültige Anfrage")
		return
	}
	if !appName.MatchString(ein.Name) {
		fehler(w, http.StatusBadRequest,
			"Name: Kleinbuchstaben, Ziffern und Bindestrich, höchstens 40 Zeichen (z.B. smerp)")
		return
	}
	k, err := s.opt.Schluessel.Anlegen(r.Context(), ein.Name)
	if err != nil {
		s.serverfehler(w, err)
		return
	}
	s.opt.Log.Info("api-schlüssel angelegt", "app", ein.Name, "von", r.RemoteAddr)
	jsonAntwort(w, http.StatusOK, map[string]string{"name": ein.Name, "schluessel": k})
}

func (s *server) sperren(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.opt.Schluessel.Sperren(r.Context(), name); err != nil {
		fehler(w, http.StatusNotFound, "Applikation nicht gefunden")
		return
	}
	s.opt.Log.Info("api-schlüssel gesperrt", "app", name, "von", r.RemoteAddr)
	jsonAntwort(w, http.StatusOK, map[string]string{"name": name})
}

func (s *server) serverfehler(w http.ResponseWriter, err error) {
	s.opt.Log.Error("verwaltung", "fehler", err)
	fehler(w, http.StatusInternalServerError, "interner Fehler")
}

func jsonAntwort(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fehler(w http.ResponseWriter, status int, text string) {
	jsonAntwort(w, status, map[string]string{"fehler": text})
}
