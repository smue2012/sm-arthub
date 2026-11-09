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
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/smue2012/sm-arthub/internal/importer"
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

// Importe: was die Oberfläche vom Importdienst braucht.
type Importe interface {
	Lieferanten(ctx context.Context) ([]importer.Quelle, error)
	LieferantAnlegen(ctx context.Context, kuerzel, name, format string) error
	ImportSpeichern(ctx context.Context, kuerzel string, e importer.Eingabe) error
	Fingerabdruck(ctx context.Context, adresse string) (string, string, error)
	Starten(kuerzel, art string, datei []byte) (int64, error)
	Laeufe(ctx context.Context, kuerzel string, limit int) ([]importer.Lauf, error)
	Lauf(ctx context.Context, id int64) (importer.Lauf, error)
	Details(ctx context.Context, laufID int64, art string, limit int) (map[string]int, []importer.Detail, error)
}

// Optionen der Oberfläche.
type Optionen struct {
	Passwort   string
	Schluessel Schluessel
	Status     StatusQuelle
	Importe    Importe
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
	if opt.Importe != nil {
		api.Handle("GET /api/lieferanten", s.nurAngemeldet(s.lieferanten))
		api.Handle("POST /api/lieferanten", s.nurAngemeldet(s.lieferantAnlegen))
		api.Handle("PUT /api/lieferanten/{kuerzel}/import", s.nurAngemeldet(s.importSpeichern))
		api.Handle("POST /api/fingerabdruck", s.nurAngemeldet(s.fingerabdruck))
		api.Handle("POST /api/lieferanten/{kuerzel}/importieren", s.nurAngemeldet(s.importieren))
		api.Handle("POST /api/lieferanten/{kuerzel}/hochladen", s.nurAngemeldet(s.hochladen))
		api.Handle("GET /api/laeufe", s.nurAngemeldet(s.laeufe))
		api.Handle("GET /api/laeufe/{id}/details", s.nurAngemeldet(s.details))
		api.Handle("GET /api/laeufe/{id}/details.csv", s.nurAngemeldet(s.detailsCSV))
	}

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

// ---------- Lieferanten und Importe ----------

// importFehler: Meldungen des Importers an die Oberfläche, sonst interner Fehler.
func (s *server) importFehler(w http.ResponseWriter, err error) {
	var f importer.Fehler
	if errors.As(err, &f) {
		fehler(w, http.StatusBadRequest, string(f))
		return
	}
	s.serverfehler(w, err)
}

func (s *server) lieferanten(w http.ResponseWriter, r *http.Request) {
	liste, err := s.opt.Importe.Lieferanten(r.Context())
	if err != nil {
		s.serverfehler(w, err)
		return
	}
	jsonAntwort(w, http.StatusOK, map[string]any{"lieferanten": liste, "formate": importer.Formate})
}

func (s *server) lieferantAnlegen(w http.ResponseWriter, r *http.Request) {
	var ein struct {
		Kuerzel string `json:"kuerzel"`
		Name    string `json:"name"`
		Format  string `json:"format"`
	}
	if err := lesen(r, &ein); err != nil {
		fehler(w, http.StatusBadRequest, "ungültige Anfrage")
		return
	}
	if err := s.opt.Importe.LieferantAnlegen(r.Context(), ein.Kuerzel, ein.Name, ein.Format); err != nil {
		s.importFehler(w, err)
		return
	}
	s.opt.Log.Info("lieferant angelegt", "kuerzel", ein.Kuerzel, "von", r.RemoteAddr)
	jsonAntwort(w, http.StatusOK, map[string]string{"kuerzel": ein.Kuerzel})
}

func (s *server) importSpeichern(w http.ResponseWriter, r *http.Request) {
	var ein importer.Eingabe
	if err := lesen(r, &ein); err != nil {
		fehler(w, http.StatusBadRequest, "ungültige Anfrage")
		return
	}
	if err := s.opt.Importe.ImportSpeichern(r.Context(), r.PathValue("kuerzel"), ein); err != nil {
		s.importFehler(w, err)
		return
	}
	s.opt.Log.Info("import gespeichert", "lieferant", r.PathValue("kuerzel"), "von", r.RemoteAddr)
	jsonAntwort(w, http.StatusOK, map[string]bool{"gespeichert": true})
}

func (s *server) fingerabdruck(w http.ResponseWriter, r *http.Request) {
	var ein struct {
		URL string `json:"url"`
	}
	if err := lesen(r, &ein); err != nil {
		fehler(w, http.StatusBadRequest, "ungültige Anfrage")
		return
	}
	fp, art, err := s.opt.Importe.Fingerabdruck(r.Context(), ein.URL)
	if err != nil {
		s.importFehler(w, err)
		return
	}
	jsonAntwort(w, http.StatusOK, map[string]string{"fingerabdruck": fp, "art": art})
}

func (s *server) importieren(w http.ResponseWriter, r *http.Request) {
	id, err := s.opt.Importe.Starten(r.PathValue("kuerzel"), "abruf", nil)
	if err != nil {
		s.importFehler(w, err)
		return
	}
	s.opt.Log.Info("import von hand gestartet", "lieferant", r.PathValue("kuerzel"), "von", r.RemoteAddr)
	jsonAntwort(w, http.StatusOK, map[string]int64{"lauf": id})
}

func (s *server) hochladen(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, importer.MaxDatei+1<<20)
	f, _, err := r.FormFile("datei")
	if err != nil {
		fehler(w, http.StatusBadRequest, "Datei fehlt oder ist grösser als 300 MB")
		return
	}
	defer f.Close()
	daten, err := io.ReadAll(io.LimitReader(f, importer.MaxDatei+1))
	if err != nil || len(daten) == 0 || len(daten) > importer.MaxDatei {
		fehler(w, http.StatusBadRequest, "Datei leer oder grösser als 300 MB")
		return
	}
	if r.MultipartForm != nil {
		r.MultipartForm.RemoveAll()
	}
	id, err := s.opt.Importe.Starten(r.PathValue("kuerzel"), "datei", daten)
	if err != nil {
		s.importFehler(w, err)
		return
	}
	s.opt.Log.Info("import aus datei gestartet", "lieferant", r.PathValue("kuerzel"), "bytes", len(daten), "von", r.RemoteAddr)
	jsonAntwort(w, http.StatusOK, map[string]int64{"lauf": id})
}

func (s *server) laeufe(w http.ResponseWriter, r *http.Request) {
	liste, err := s.opt.Importe.Laeufe(r.Context(), r.URL.Query().Get("lieferant"), 20)
	if err != nil {
		s.serverfehler(w, err)
		return
	}
	jsonAntwort(w, http.StatusOK, map[string]any{"laeufe": liste})
}

// laufUndArt liest Lauf-ID und Art aus der Anfrage.
func (s *server) laufUndArt(w http.ResponseWriter, r *http.Request) (importer.Lauf, string, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fehler(w, http.StatusBadRequest, "ungültige Lauf-Nummer")
		return importer.Lauf{}, "", false
	}
	art := r.URL.Query().Get("art")
	if art != "" {
		ok := false
		for _, a := range importer.DetailArten {
			ok = ok || a.Art == art
		}
		if !ok {
			fehler(w, http.StatusBadRequest, "unbekannte Art")
			return importer.Lauf{}, "", false
		}
	}
	lauf, err := s.opt.Importe.Lauf(r.Context(), id)
	if err != nil {
		s.importFehler(w, err)
		return importer.Lauf{}, "", false
	}
	return lauf, art, true
}

// MaxDetailsAnzeige: so viele Zeilen zeigt die Oberfläche, alle gibt es als CSV.
const MaxDetailsAnzeige = 500

func (s *server) details(w http.ResponseWriter, r *http.Request) {
	lauf, art, ok := s.laufUndArt(w, r)
	if !ok {
		return
	}
	anzahl, liste, err := s.opt.Importe.Details(r.Context(), lauf.ID, art, MaxDetailsAnzeige)
	if err != nil {
		s.serverfehler(w, err)
		return
	}
	arten := []map[string]any{}
	for _, a := range importer.DetailArten {
		arten = append(arten, map[string]any{"art": a.Art, "text": a.Text, "anzahl": anzahl[a.Art]})
	}
	jsonAntwort(w, http.StatusOK, map[string]any{"lauf": lauf, "arten": arten, "details": liste, "max": MaxDetailsAnzeige})
}

func (s *server) detailsCSV(w http.ResponseWriter, r *http.Request) {
	lauf, art, ok := s.laufUndArt(w, r)
	if !ok {
		return
	}
	_, liste, err := s.opt.Importe.Details(r.Context(), lauf.ID, art, 0)
	if err != nil {
		s.serverfehler(w, err)
		return
	}
	texte := map[string]string{}
	for _, a := range importer.DetailArten {
		texte[a.Art] = a.Text
	}
	name := fmt.Sprintf("import-%s-%s-%d", lauf.Lieferant, lauf.Start.Local().Format("2006-01-02"), lauf.ID)
	if art != "" {
		name += "-" + art
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	w.Write([]byte("\xef\xbb\xbf")) // BOM, damit Excel UTF-8 erkennt
	c := csv.NewWriter(w)
	c.Comma = ';'
	c.Write([]string{"Art", "Lieferanten-Nr.", "Artikelnummer", "Bezeichnung", "Alt", "Neu"})
	for _, d := range liste {
		c.Write([]string{texte[d.Art], csvSicher(d.LieferantenNr), csvSicher(d.ArtikelNummer), csvSicher(d.Bezeichnung),
			csvSicher(d.Alt), csvSicher(d.Neu)})
	}
	c.Flush()
}

// csvSicher verhindert, dass Excel einen Text als Formel ausführt.
func csvSicher(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
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
