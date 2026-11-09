// Paket katalog liest Artikel und Lieferantenangebote aus der Datenbank.
//
// Bestpreis-Regel (Entscheid Stephan, 09.11.2026): Massgebend ist der tiefste
// EK eines verfügbaren Angebots (Lagerbestand > 0). Bei gleichem EK gewinnt
// der höhere Lagerbestand. Ist kein Angebot verfügbar, wird das günstigste
// trotzdem geliefert, aber mit verfuegbar = false.
package katalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// ErrNichtGefunden: Artikelnummer unbekannt.
var ErrNichtGefunden = errors.New("artikel nicht gefunden")

// Angebot eines Lieferanten. Der EK kommt als Text (z.B. "12.3400"), damit
// keine Rundung über Gleitkommazahlen entsteht.
type Angebot struct {
	Lieferant      string     `json:"lieferant"` // Kürzel, z.B. alltron
	LieferantName  string     `json:"lieferant_name"`
	LieferantenNr  string     `json:"lieferanten_nr"`
	EK             string     `json:"ek"`
	Waehrung       string     `json:"waehrung"`
	Lagerbestand   int        `json:"lagerbestand"`
	Verfuegbar     bool       `json:"verfuegbar"`
	LieferfristTg  *int       `json:"lieferfrist_tage"`
	BildURLs       []string   `json:"bild_urls"`
	StandLieferant *time.Time `json:"stand_lieferant"`
}

// Artikel mit allen aktiven Angeboten, sortiert nach der Bestpreis-Regel.
// Bestes ist das erste Angebot (nil, wenn es keins gibt).
type Artikel struct {
	Nummer       string    `json:"nummer"`
	Art          string    `json:"art"` // ware | software
	Bezeichnung  string    `json:"bezeichnung"`
	Beschreibung string    `json:"beschreibung"`
	Hersteller   string    `json:"hersteller"`
	HerstellerNr string    `json:"hersteller_nr"`
	EAN          string    `json:"ean"`
	Kategorie    string    `json:"kategorie"`
	GewichtG     *int      `json:"gewicht_g"`
	LaengeMM     *int      `json:"laenge_mm"`
	BreiteMM     *int      `json:"breite_mm"`
	HoeheMM      *int      `json:"hoehe_mm"`
	Aktiv        bool      `json:"aktiv"`
	Stand        int64     `json:"stand"`
	Geaendert    time.Time `json:"geaendert"`
	Bestes       *Angebot  `json:"bestes"`
	Angebote     []Angebot `json:"angebote"`

	id int64
}

// Lieferant für die Liste der Quellen.
type Lieferant struct {
	Kuerzel string `json:"kuerzel"`
	Name    string `json:"name"`
	Aktiv   bool   `json:"aktiv"`
}

// Repository liest aus PostgreSQL.
type Repository struct {
	DB *sql.DB
}

const artikelSpalten = `id, nummer, art, bezeichnung, beschreibung, hersteller, hersteller_nr,
	ean, kategorie, gewicht_g, laenge_mm, breite_mm, hoehe_mm, aktiv, stand, geaendert`

const artikelSpaltenA = `a.id, a.nummer, a.art, a.bezeichnung, a.beschreibung, a.hersteller, a.hersteller_nr,
	a.ean, a.kategorie, a.gewicht_g, a.laenge_mm, a.breite_mm, a.hoehe_mm, a.aktiv, a.stand, a.geaendert`

func scanArtikel(s interface{ Scan(...any) error }) (Artikel, error) {
	var a Artikel
	err := s.Scan(&a.id, &a.Nummer, &a.Art, &a.Bezeichnung, &a.Beschreibung, &a.Hersteller,
		&a.HerstellerNr, &a.EAN, &a.Kategorie, &a.GewichtG, &a.LaengeMM, &a.BreiteMM, &a.HoeheMM,
		&a.Aktiv, &a.Stand, &a.Geaendert)
	return a, err
}

// Seit liefert bis zu limit Artikel mit stand > seit (auch inaktive), aufsteigend
// nach Stand. mehr = es gibt weitere; dann mit dem Stand des letzten weiterfragen.
func (r Repository) Seit(ctx context.Context, seit int64, limit int) (liste []Artikel, mehr bool, err error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+artikelSpalten+` FROM artikel
		WHERE stand > $1 ORDER BY stand LIMIT $2`, seit, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanArtikel(rows)
		if err != nil {
			return nil, false, err
		}
		liste = append(liste, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(liste) > limit {
		liste, mehr = liste[:limit], true
	}
	if err := r.angeboteLaden(ctx, liste); err != nil {
		return nil, false, err
	}
	return liste, mehr, nil
}

// Nummer liefert einen Artikel über seine Hub-Nummer.
func (r Repository) Nummer(ctx context.Context, nummer string) (Artikel, error) {
	a, err := scanArtikel(r.DB.QueryRowContext(ctx,
		`SELECT `+artikelSpalten+` FROM artikel WHERE nummer = $1`, nummer))
	if errors.Is(err, sql.ErrNoRows) {
		return Artikel{}, ErrNichtGefunden
	}
	if err != nil {
		return Artikel{}, err
	}
	liste := []Artikel{a}
	if err := r.angeboteLaden(ctx, liste); err != nil {
		return Artikel{}, err
	}
	return liste[0], nil
}

// Suche findet aktive Artikel, in denen jedes Wort von q vorkommt (Bezeichnung,
// Beschreibung, Hersteller, Herstellernummer, EAN, Hub-Nummer oder
// Lieferanten-Artikelnummer). nurVerfuegbar: nur Artikel mit einem Angebot an
// Lager. Sortiert nach Bezeichnung; mehr = es gäbe weitere Treffer.
func (r Repository) Suche(ctx context.Context, q string, limit int, nurVerfuegbar bool) (liste []Artikel, mehr bool, err error) {
	woerter := strings.Fields(q)
	if len(woerter) == 0 {
		return []Artikel{}, false, nil
	}
	if len(woerter) > 8 {
		woerter = woerter[:8]
	}
	var bed []string
	args := []any{}
	for _, w := range woerter {
		args = append(args, "%"+likeMaskieren(w)+"%")
		n := len(args)
		bed = append(bed, fmt.Sprintf(`(a.bezeichnung ILIKE $%[1]d OR a.beschreibung ILIKE $%[1]d
			OR a.hersteller ILIKE $%[1]d OR a.hersteller_nr ILIKE $%[1]d OR a.ean ILIKE $%[1]d
			OR a.nummer ILIKE $%[1]d OR EXISTS (SELECT 1 FROM angebote x
				WHERE x.artikel_id = a.id AND x.lieferanten_nr ILIKE $%[1]d))`, n))
	}
	if nurVerfuegbar {
		bed = append(bed, `EXISTS (SELECT 1 FROM angebote v JOIN lieferanten vl ON vl.id = v.lieferant_id AND vl.aktiv
			WHERE v.artikel_id = a.id AND v.aktiv AND v.lagerbestand > 0)`)
	}
	args = append(args, limit+1)
	rows, err := r.DB.QueryContext(ctx, `SELECT `+artikelSpaltenA+` FROM artikel a
		WHERE a.aktiv AND `+strings.Join(bed, " AND ")+`
		ORDER BY a.bezeichnung, a.nummer LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	liste = []Artikel{}
	for rows.Next() {
		a, err := scanArtikel(rows)
		if err != nil {
			return nil, false, err
		}
		liste = append(liste, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(liste) > limit {
		liste, mehr = liste[:limit], true
	}
	if err := r.angeboteLaden(ctx, liste); err != nil {
		return nil, false, err
	}
	return liste, mehr, nil
}

// likeMaskieren: % und _ im Suchbegriff wörtlich nehmen.
func likeMaskieren(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// Lieferanten liefert alle Lieferanten.
func (r Repository) Lieferanten(ctx context.Context) ([]Lieferant, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT kuerzel, name, aktiv FROM lieferanten ORDER BY kuerzel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	liste := []Lieferant{}
	for rows.Next() {
		var l Lieferant
		if err := rows.Scan(&l.Kuerzel, &l.Name, &l.Aktiv); err != nil {
			return nil, err
		}
		liste = append(liste, l)
	}
	return liste, rows.Err()
}

// angeboteLaden hängt die aktiven Angebote aktiver Lieferanten an, sortiert
// nach der Bestpreis-Regel, und setzt Bestes.
func (r Repository) angeboteLaden(ctx context.Context, liste []Artikel) error {
	if len(liste) == 0 {
		return nil
	}
	ids := make([]int64, len(liste))
	index := make(map[int64]int, len(liste))
	for i, a := range liste {
		ids[i] = a.id
		index[a.id] = i
		liste[i].Angebote = []Angebot{}
	}
	rows, err := r.DB.QueryContext(ctx, `
		SELECT an.artikel_id, l.kuerzel, l.name, an.lieferanten_nr, an.ek::text, an.waehrung,
		       an.lagerbestand, an.lieferfrist_tage, an.bild_urls, an.stand_lieferant
		FROM angebote an
		JOIN lieferanten l ON l.id = an.lieferant_id AND l.aktiv
		WHERE an.aktiv AND an.artikel_id = ANY($1)
		ORDER BY an.artikel_id, (an.lagerbestand > 0) DESC, an.ek, an.lagerbestand DESC, l.kuerzel`,
		pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var an Angebot
		if err := rows.Scan(&id, &an.Lieferant, &an.LieferantName, &an.LieferantenNr, &an.EK,
			&an.Waehrung, &an.Lagerbestand, &an.LieferfristTg, pq.Array(&an.BildURLs),
			&an.StandLieferant); err != nil {
			return err
		}
		if an.BildURLs == nil {
			an.BildURLs = []string{}
		}
		an.Verfuegbar = an.Lagerbestand > 0
		i := index[id]
		liste[i].Angebote = append(liste[i].Angebote, an)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range liste {
		if len(liste[i].Angebote) > 0 {
			b := liste[i].Angebote[0]
			liste[i].Bestes = &b
		}
	}
	return nil
}
