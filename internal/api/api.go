// Paket api stellt die Lese-API für die Applikationen bereit (Version 1).
//
// Regeln: nur lesen, Zugriff nur mit API-Schlüssel (Authorization: Bearer …),
// nur rückwärtskompatibel ändern, unbekannte Parameter ablehnen.
//
//	GET /api/v1/artikel?seit=<stand>&limit=<n>   Delta-Abgleich
//	GET /api/v1/artikel/{nummer}                 ein Artikel
//	GET /api/v1/lieferanten                      Quellen
//	GET /health                                  ohne Schlüssel, nur «ok»
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/smue2012/sm-arthub/internal/katalog"
	"github.com/smue2012/sm-arthub/internal/schluessel"
)

// Anzahl Artikel je Abfrage.
const (
	StandardLimit = 500
	MaxLimit      = 2000
)

// Katalog: was die API vom Katalog braucht (im Test eine Attrappe).
type Katalog interface {
	Seit(ctx context.Context, seit int64, limit int) ([]katalog.Artikel, bool, error)
	Nummer(ctx context.Context, nummer string) (katalog.Artikel, error)
	Lieferanten(ctx context.Context) ([]katalog.Lieferant, error)
}

// Pruefer: liefert zum Schlüssel den Namen der App, bei unbekanntem oder
// gesperrtem Schlüssel schluessel.ErrUngueltig.
type Pruefer interface {
	Pruefen(ctx context.Context, schluessel string) (string, error)
}

type appKey struct{}

// App liefert den Namen der aufrufenden App (für Logs).
func App(ctx context.Context) string {
	s, _ := ctx.Value(appKey{}).(string)
	return s
}

// Handler baut alle Routen.
func Handler(k Katalog, p Pruefer, log *slog.Logger) http.Handler {
	h := handler{k: k, log: log}
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /api/v1/artikel", h.artikelSeit)
	v1.HandleFunc("GET /api/v1/artikel/{nummer}", h.artikel)
	v1.HandleFunc("GET /api/v1/lieferanten", h.lieferanten)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/api/v1/", mitSchluessel(p, log, v1))
	return mux
}

func mitSchluessel(p Pruefer, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			fehler(w, http.StatusUnauthorized, "API-Schlüssel fehlt")
			return
		}
		app, err := p.Pruefen(r.Context(), strings.TrimSpace(token))
		if err != nil {
			if !errors.Is(err, schluessel.ErrUngueltig) {
				log.Error("schlüssel prüfen", "fehler", err)
			}
			fehler(w, http.StatusUnauthorized, "API-Schlüssel ungültig")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), appKey{}, app)))
	})
}

type handler struct {
	k   Katalog
	log *slog.Logger
}

// nurParameter lehnt unbekannte Abfrageparameter ab.
func nurParameter(w http.ResponseWriter, r *http.Request, erlaubt ...string) bool {
	for name := range r.URL.Query() {
		ok := false
		for _, e := range erlaubt {
			if name == e {
				ok = true
				break
			}
		}
		if !ok {
			fehler(w, http.StatusBadRequest, "unbekannter Parameter: "+name)
			return false
		}
	}
	return true
}

type seitAntwort struct {
	Artikel []katalog.Artikel `json:"artikel"`
	Stand   int64             `json:"stand"` // für die nächste Abfrage als «seit»
	Mehr    bool              `json:"mehr"`  // true = sofort mit «seit=stand» weiterfragen
}

func (h handler) artikelSeit(w http.ResponseWriter, r *http.Request) {
	if !nurParameter(w, r, "seit", "limit") {
		return
	}
	q := r.URL.Query()
	var seit int64
	if s := q.Get("seit"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			fehler(w, http.StatusBadRequest, "seit muss eine Zahl ≥ 0 sein")
			return
		}
		seit = v
	}
	limit := StandardLimit
	if s := q.Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 || v > MaxLimit {
			fehler(w, http.StatusBadRequest, "limit muss zwischen 1 und "+strconv.Itoa(MaxLimit)+" liegen")
			return
		}
		limit = v
	}
	liste, mehr, err := h.k.Seit(r.Context(), seit, limit)
	if err != nil {
		h.serverfehler(w, r, err)
		return
	}
	stand := seit
	if len(liste) > 0 {
		stand = liste[len(liste)-1].Stand
	}
	if liste == nil {
		liste = []katalog.Artikel{}
	}
	json200(w, seitAntwort{Artikel: liste, Stand: stand, Mehr: mehr})
}

func (h handler) artikel(w http.ResponseWriter, r *http.Request) {
	if !nurParameter(w, r) {
		return
	}
	a, err := h.k.Nummer(r.Context(), r.PathValue("nummer"))
	if errors.Is(err, katalog.ErrNichtGefunden) {
		fehler(w, http.StatusNotFound, "Artikel nicht gefunden")
		return
	}
	if err != nil {
		h.serverfehler(w, r, err)
		return
	}
	json200(w, a)
}

func (h handler) lieferanten(w http.ResponseWriter, r *http.Request) {
	if !nurParameter(w, r) {
		return
	}
	liste, err := h.k.Lieferanten(r.Context())
	if err != nil {
		h.serverfehler(w, r, err)
		return
	}
	json200(w, map[string]any{"lieferanten": liste})
}

func (h handler) serverfehler(w http.ResponseWriter, r *http.Request, err error) {
	h.log.Error("anfrage", "pfad", r.URL.Path, "app", App(r.Context()), "fehler", err)
	fehler(w, http.StatusInternalServerError, "interner Fehler")
}

func json200(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func fehler(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"fehler": text})
}
