package katalog

// Auswahl je App (Wunsch Stephan, 09.11.2026): Im Artikelbrowser der
// Verwaltung werden für jede App (API-Schlüssel) Kategorien und Einzelartikel
// angehakt. Hat die App «nur Auswahl», liefert die Lese-API ihr nur diese
// Artikel; sonst den ganzen Katalog.
//
// Kategorien sind Pfade «Ebene 1», «Ebene 1 > Ebene 2» oder
// «Ebene 1 > Ebene 2 > Ebene 3» und umfassen den ganzen Zweig.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Trenner zwischen den Kategorie-Ebenen in Pfaden.
const Trenner = " > "

// AuswahlBedingung: SQL-Bedingung für Artikel «a», die die App mit dem Namen in
// Parameter $n sehen darf. Leerer Name (interne Aufrufe) = alles.
func AuswahlBedingung(n int) string {
	return fmt.Sprintf(`(NOT coalesce((SELECT k.nur_auswahl FROM api_schluessel k WHERE k.name = $%[1]d), false)
		OR %[2]s)`, n, inAuswahl(n))
}

// inAuswahl: Artikel «a» ist in der Auswahl der App $n (unabhängig vom Modus).
func inAuswahl(n int) string {
	return fmt.Sprintf(`(a.nummer IN (SELECT w.wert FROM auswahl w WHERE w.app = $%[1]d AND w.art = 'artikel')
		OR EXISTS (SELECT 1 FROM auswahl w WHERE w.app = $%[1]d AND w.art = 'kategorie'
			AND starts_with(a.kategorie || ' > ' || a.kategorie2 || ' > ' || a.kategorie3 || ' >', w.wert || ' >')))`, n)
}

// ArtikelKurz: ein einzeln gewählter Artikel.
type ArtikelKurz struct {
	Nummer      string `json:"nummer"`
	Bezeichnung string `json:"bezeichnung"`
	Aktiv       bool   `json:"aktiv"`
}

// Auswahl einer App.
type Auswahl struct {
	App        string        `json:"app"`
	NurAuswahl bool          `json:"nur_auswahl"`
	Kategorien []string      `json:"kategorien"`
	Artikel    []ArtikelKurz `json:"artikel"`
	Anzahl     int           `json:"anzahl"` // aktive Artikel in der Auswahl
}

// ErrAppUnbekannt: kein API-Schlüssel mit diesem Namen.
var ErrAppUnbekannt = errors.New("app unbekannt")

func (r Repository) appPruefen(ctx context.Context, app string) error {
	var x int
	err := r.DB.QueryRowContext(ctx, `SELECT 1 FROM api_schluessel WHERE name = $1`, app).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAppUnbekannt
	}
	return err
}

// AuswahlLesen liefert die Auswahl einer App.
func (r Repository) AuswahlLesen(ctx context.Context, app string) (Auswahl, error) {
	a := Auswahl{App: app, Kategorien: []string{}, Artikel: []ArtikelKurz{}}
	err := r.DB.QueryRowContext(ctx, `SELECT nur_auswahl FROM api_schluessel WHERE name = $1`, app).Scan(&a.NurAuswahl)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrAppUnbekannt
	}
	if err != nil {
		return a, err
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT wert FROM auswahl WHERE app = $1 AND art = 'kategorie' ORDER BY wert`, app)
	if err != nil {
		return a, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return a, err
		}
		a.Kategorien = append(a.Kategorien, k)
	}
	rows.Close()
	rows, err = r.DB.QueryContext(ctx, `SELECT w.wert, coalesce(ar.bezeichnung, '(nicht mehr im Katalog)'), coalesce(ar.aktiv, false)
		FROM auswahl w LEFT JOIN artikel ar ON ar.nummer = w.wert
		WHERE w.app = $1 AND w.art = 'artikel' ORDER BY ar.bezeichnung`, app)
	if err != nil {
		return a, err
	}
	for rows.Next() {
		var x ArtikelKurz
		if err := rows.Scan(&x.Nummer, &x.Bezeichnung, &x.Aktiv); err != nil {
			rows.Close()
			return a, err
		}
		a.Artikel = append(a.Artikel, x)
	}
	rows.Close()
	err = r.DB.QueryRowContext(ctx, `SELECT count(*) FROM artikel a WHERE a.aktiv AND `+inAuswahl(1), app).Scan(&a.Anzahl)
	return a, err
}

// AuswahlModus schaltet «nur Auswahl» einer App ein oder aus.
func (r Repository) AuswahlModus(ctx context.Context, app string, nurAuswahl bool) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE api_schluessel SET nur_auswahl = $2 WHERE name = $1`, app, nurAuswahl)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAppUnbekannt
	}
	return nil
}

// KategoriePfad baut den Pfad aus den Ebenen (leere Ebenen am Ende fallen weg).
func KategoriePfad(ebenen ...string) string {
	var teile []string
	for _, e := range ebenen {
		e = strings.TrimSpace(e)
		if e == "" {
			break
		}
		teile = append(teile, e)
	}
	return strings.Join(teile, Trenner)
}

// AuswahlSetzen nimmt eine Kategorie (Pfad) oder einen Artikel (Hub-Nummer) in
// die Auswahl der App auf (an) oder entfernt sie.
func (r Repository) AuswahlSetzen(ctx context.Context, app, art, wert string, an bool) error {
	if art != "kategorie" && art != "artikel" {
		return fmt.Errorf("art %q", art)
	}
	wert = strings.TrimSpace(wert)
	if wert == "" || len(wert) > 700 {
		return fmt.Errorf("wert leer oder zu lang")
	}
	if err := r.appPruefen(ctx, app); err != nil {
		return err
	}
	if !an {
		_, err := r.DB.ExecContext(ctx, `DELETE FROM auswahl WHERE app = $1 AND art = $2 AND wert = $3`, app, art, wert)
		return err
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO auswahl (app, art, wert) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		app, art, wert)
	return err
}

// Kategorie: ein Knoten des Kategoriebaums mit Anzahl aktiver Artikel.
type Kategorie struct {
	Name   string       `json:"name"`
	Pfad   string       `json:"pfad"`
	Anzahl int          `json:"anzahl"`
	Unter  []*Kategorie `json:"unter,omitempty"`
}

// Kategorien liefert den Baum der aktiven Artikel (drei Ebenen).
func (r Repository) Kategorien(ctx context.Context) ([]*Kategorie, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT kategorie, kategorie2, kategorie3, count(*) FROM artikel
		WHERE aktiv GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wurzel := &Kategorie{}
	index := map[string]*Kategorie{"": wurzel}
	for rows.Next() {
		var k1, k2, k3 string
		var n int
		if err := rows.Scan(&k1, &k2, &k3, &n); err != nil {
			return nil, err
		}
		if strings.TrimSpace(k1) == "" {
			k1, k2, k3 = "(ohne Kategorie)", "", ""
		}
		eltern := wurzel
		eltern.Anzahl += n
		for i, name := range []string{k1, k2, k3} {
			if strings.TrimSpace(name) == "" {
				break
			}
			pfad := KategoriePfad([]string{k1, k2, k3}[:i+1]...)
			k, ok := index[pfad]
			if !ok {
				k = &Kategorie{Name: name, Pfad: pfad}
				index[pfad] = k
				eltern.Unter = append(eltern.Unter, k)
			}
			k.Anzahl += n
			eltern = k
		}
	}
	if wurzel.Unter == nil {
		return []*Kategorie{}, rows.Err()
	}
	return wurzel.Unter, rows.Err()
}

// Blaettern: Artikelliste für den Browser (alle aktiven, unabhängig von einer
// App), gefiltert nach Kategoriepfad und Suchwörtern, seitenweise.
func (r Repository) Blaettern(ctx context.Context, pfad, q string, nurVerfuegbar bool, seite, proSeite int) ([]Artikel, int, error) {
	var bed []string
	var args []any
	if pfad = strings.TrimSpace(pfad); pfad != "" {
		if pfad == "(ohne Kategorie)" {
			bed = append(bed, `a.kategorie = ''`)
		} else {
			args = append(args, pfad+" >")
			bed = append(bed, fmt.Sprintf(`starts_with(a.kategorie || ' > ' || a.kategorie2 || ' > ' || a.kategorie3 || ' >', $%d)`, len(args)))
		}
	}
	for _, w := range strings.Fields(q) {
		if len(bed) > 12 {
			break
		}
		args = append(args, "%"+likeMaskieren(strings.ToLower(w))+"%")
		bed = append(bed, fmt.Sprintf(`a.id IN (SELECT s.artikel_id FROM artikel_suche s WHERE s.suchtext LIKE $%d)`, len(args)))
	}
	if nurVerfuegbar {
		bed = append(bed, `EXISTS (SELECT 1 FROM angebote v JOIN lieferanten vl ON vl.id = v.lieferant_id AND vl.aktiv
			WHERE v.artikel_id = a.id AND v.aktiv AND v.lagerbestand > 0)`)
	}
	wo := "a.aktiv"
	if len(bed) > 0 {
		wo += " AND " + strings.Join(bed, " AND ")
	}
	var gesamt int
	if err := r.DB.QueryRowContext(ctx, `SELECT count(*) FROM artikel a WHERE `+wo, args...).Scan(&gesamt); err != nil {
		return nil, 0, err
	}
	args = append(args, proSeite, seite*proSeite)
	rows, err := r.DB.QueryContext(ctx, `SELECT `+artikelSpaltenA+` FROM artikel a WHERE `+wo+
		fmt.Sprintf(` ORDER BY a.bezeichnung, a.nummer LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	liste := []Artikel{}
	for rows.Next() {
		a, err := scanArtikel(rows)
		if err != nil {
			return nil, 0, err
		}
		liste = append(liste, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return liste, gesamt, r.angeboteLaden(ctx, liste)
}
