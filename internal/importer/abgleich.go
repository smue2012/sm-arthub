package importer

// Abgleich einer Preisliste mit dem Katalog – mengenbasiert in einer
// Transaktion (COPY in eine Temporärtabelle, dann SQL), damit auch 236'000
// Zeilen in Sekunden durchgehen.
//
//  1. Angebote, die es schon gibt (Lieferant + Lieferanten-Nr.), werden
//     nachgeführt (EK, Lagerbestand, Lieferfrist, wieder aktiv).
//  2. Stammdaten von Artikeln, die diese Liste angelegt hat, werden nachgeführt.
//  3. Neue Angebote suchen ihren Artikel: gleiche EAN (nur wenn eindeutig),
//     sonst gleicher Hersteller + Herstellernummer (nur wenn eindeutig),
//     sonst wird ein neuer Artikel angelegt (gleiche EAN in der Datei = ein Artikel).
//  4. Angebote, die nicht mehr in der Liste sind, werden inaktiv.
//  5. Artikel sind aktiv, solange sie ein aktives Angebot haben; jeder
//     betroffene Artikel bekommt einen neuen Stand (Delta-Abgleich der Apps).
//
// Schutz: Enthält die Datei weniger als die Hälfte der bisher aktiven Angebote
// (abgebrochene Übertragung), wird nichts geändert.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// Bericht eines Laufs (auch in import_laeufe.bericht).
type Bericht struct {
	Zeilen              int      `json:"zeilen"`
	Uebersprungen       int      `json:"uebersprungen"`
	ArtikelNeu          int      `json:"artikel_neu"`
	AngeboteNeu         int      `json:"angebote_neu"`
	Zugeordnet          int      `json:"zugeordnet"`         // neue Angebote zu bestehenden Artikeln
	AngeboteGeaendert   int      `json:"angebote_geaendert"` // EK, Bestand oder Lieferfrist
	PreiseGeaendert     int      `json:"preise_geaendert"`
	BestandGeaendert    int      `json:"bestand_geaendert"` // nur Bestand/Lieferfrist
	Reaktiviert         int      `json:"reaktiviert"`
	StammdatenGeaendert int      `json:"stammdaten_geaendert"`
	Deaktiviert         int      `json:"deaktiviert"`
	BilderGeaendert     int      `json:"bilder_geaendert"`
	EANMehrdeutig       int      `json:"ean_mehrdeutig"` // EAN gehört mehreren Artikeln – nicht darüber zugeordnet
	Sekunden            float64  `json:"sekunden"`
	Hinweise            []string `json:"hinweise,omitempty"`
}

// Abgleichen schreibt die Zeilen einer Liste für den Lieferanten in den Katalog.
// bilder (optional): Bild-Links je Lieferanten-Nr. aus der Bildliste.
func Abgleichen(ctx context.Context, db *sql.DB, lieferantID int, zeilen []Zeile,
	bilder map[string][]string, dateiStand time.Time) (Bericht, error) {
	return AbgleichenMitProtokoll(ctx, db, lieferantID, 0, zeilen, bilder, dateiStand)
}

// AbgleichenMitProtokoll wie Abgleichen; mit laufID > 0 wird jede Änderung
// (ausser reinen Bestandsänderungen) in import_details protokolliert.
func AbgleichenMitProtokoll(ctx context.Context, db *sql.DB, lieferantID int, laufID int64, zeilen []Zeile,
	bilder map[string][]string, dateiStand time.Time) (Bericht, error) {

	start := time.Now()
	b := Bericht{Zeilen: len(zeilen)}
	var stand any
	if !dateiStand.IsZero() {
		stand = dateiStand
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return b, err
	}
	defer tx.Rollback()

	var bisher int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM angebote WHERE lieferant_id = $1 AND aktiv`,
		lieferantID).Scan(&bisher); err != nil {
		return b, err
	}
	if bisher >= 1000 && len(zeilen) < bisher/2 {
		return b, Fehler(fmt.Sprintf("Die Liste enthält nur %d Artikel, bisher waren %d aktiv – "+
			"vermutlich unvollständig übertragen. Es wurde nichts geändert.", len(zeilen), bisher))
	}

	schritte := []string{
		`SET LOCAL arthub.massenimport = '1'`,
		`CREATE TEMP TABLE imp (
			lieferanten_nr TEXT PRIMARY KEY, bezeichnung TEXT, beschreibung TEXT, hersteller TEXT,
			hersteller_nr TEXT, ean TEXT, kategorie TEXT, gewicht_g INTEGER, ek NUMERIC(12,4),
			lagerbestand INTEGER, lieferfrist_tage INTEGER, bild_urls TEXT[],
			artikel_id BIGINT, neu BOOLEAN NOT NULL DEFAULT false) ON COMMIT DROP`,
		`CREATE TEMP TABLE betroffen (artikel_id BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE suche (artikel_id BIGINT) ON COMMIT DROP`, // Suchtext auffrischen
	}
	for _, s := range schritte {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return b, err
		}
	}

	copy, err := tx.PrepareContext(ctx, pq.CopyIn("imp", "lieferanten_nr", "bezeichnung", "beschreibung",
		"hersteller", "hersteller_nr", "ean", "kategorie", "gewicht_g", "ek", "lagerbestand", "lieferfrist_tage", "bild_urls"))
	if err != nil {
		return b, err
	}
	for _, z := range zeilen {
		var bildArray any
		if bilder != nil {
			bildArray = pq.Array(append([]string{}, bilder[z.LieferantenNr]...))
		}
		if _, err := copy.ExecContext(ctx, z.LieferantenNr, z.Bezeichnung, z.Beschreibung, z.Hersteller,
			z.HerstellerNr, z.EAN, z.Kategorie, z.GewichtG, z.EK, z.Lagerbestand, z.LieferfristTg, bildArray); err != nil {
			copy.Close()
			return b, err
		}
	}
	if _, err := copy.ExecContext(ctx); err != nil {
		copy.Close()
		return b, err
	}
	if err := copy.Close(); err != nil {
		return b, err
	}
	if _, err := tx.ExecContext(ctx, `ANALYZE imp`); err != nil {
		return b, err
	}

	anzahl := func(ziel *int, q string, args ...any) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if ziel != nil {
			*ziel = int(n)
		}
		return nil
	}
	zaehlen := func(ziel *int, q string, args ...any) error {
		return tx.QueryRowContext(ctx, q, args...).Scan(ziel)
	}

	protokoll := func(q string, args ...any) error {
		if laufID <= 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO import_details (lauf_id, art, lieferanten_nr, artikel_nummer,
			bezeichnung, alt, neu) `+q, args...)
		return err
	}

	// 1. bestehende Angebote (imp.artikel_id bleibt hier leer: alle 236'000 Zeilen
	// der Temporärtabelle dafür umzuschreiben kostete 1,7 s; die Schritte unten
	// verbinden direkt über Lieferant + Lieferanten-Nr.)
	if err := tx.QueryRowContext(ctx, `SELECT
			count(*) FILTER (WHERE an.aktiv AND an.ek IS DISTINCT FROM i.ek),
			count(*) FILTER (WHERE NOT an.aktiv),
			count(*) FILTER (WHERE an.aktiv AND an.ek = i.ek
				AND (an.lagerbestand, an.lieferfrist_tage) IS DISTINCT FROM (i.lagerbestand, i.lieferfrist_tage))
		FROM angebote an JOIN imp i ON i.lieferanten_nr = an.lieferanten_nr
		WHERE an.lieferant_id = $1`, lieferantID).Scan(&b.PreiseGeaendert, &b.Reaktiviert, &b.BestandGeaendert); err != nil {
		return b, err
	}
	if err := protokoll(`SELECT $2, CASE WHEN NOT an.aktiv THEN 'wieder' ELSE 'preis' END, an.lieferanten_nr,
			a.nummer, a.bezeichnung, an.ek::text, i.ek::text
		FROM angebote an JOIN imp i ON i.lieferanten_nr = an.lieferanten_nr JOIN artikel a ON a.id = an.artikel_id
		WHERE an.lieferant_id = $1 AND (NOT an.aktiv OR an.ek IS DISTINCT FROM i.ek)`, lieferantID, laufID); err != nil {
		return b, err
	}
	if err := zaehlen(&b.AngeboteGeaendert, `WITH u AS (
			UPDATE angebote an SET ek = i.ek, lagerbestand = i.lagerbestand, lieferfrist_tage = i.lieferfrist_tage,
				aktiv = true, stand_lieferant = coalesce($2, an.stand_lieferant), geaendert = now()
			FROM imp i
			WHERE an.lieferant_id = $1 AND an.lieferanten_nr = i.lieferanten_nr
			  AND (an.ek, an.lagerbestand, an.lieferfrist_tage, an.aktiv)
			      IS DISTINCT FROM (i.ek, i.lagerbestand, i.lieferfrist_tage, true)
			RETURNING an.artikel_id),
		  b AS (INSERT INTO betroffen SELECT artikel_id FROM u RETURNING 1)
		SELECT count(*) FROM b`, lieferantID, stand); err != nil {
		return b, err
	}
	if bilder != nil {
		if err := zaehlen(&b.BilderGeaendert, `WITH u AS (
				UPDATE angebote an SET bild_urls = i.bild_urls, geaendert = now()
				FROM imp i
				WHERE an.lieferant_id = $1 AND an.lieferanten_nr = i.lieferanten_nr
				  AND an.bild_urls IS DISTINCT FROM i.bild_urls
				RETURNING an.artikel_id),
			  b AS (INSERT INTO betroffen SELECT artikel_id FROM u RETURNING 1)
			SELECT count(*) FROM b`, lieferantID); err != nil {
			return b, err
		}
	}

	// 2. Stammdaten der Artikel, die diese Liste angelegt hat (Trigger setzt den Stand)
	if err := protokoll(`SELECT $2, 'stammdaten', i.lieferanten_nr, a.nummer, i.bezeichnung, a.bezeichnung, i.bezeichnung
		FROM imp i
		JOIN angebote an ON an.lieferant_id = $1 AND an.lieferanten_nr = i.lieferanten_nr
		JOIN artikel a ON a.id = an.artikel_id
		WHERE a.quelle_lieferant_id = $1
		  AND (a.bezeichnung, a.beschreibung, a.hersteller, a.hersteller_nr, a.ean, a.kategorie, a.gewicht_g)
		      IS DISTINCT FROM (i.bezeichnung, i.beschreibung, i.hersteller, i.hersteller_nr, i.ean, i.kategorie, i.gewicht_g)`,
		lieferantID, laufID); err != nil {
		return b, err
	}
	if err := zaehlen(&b.StammdatenGeaendert, `WITH u AS (
			UPDATE artikel a SET bezeichnung = i.bezeichnung,
				beschreibung = i.beschreibung, hersteller = i.hersteller, hersteller_nr = i.hersteller_nr,
				ean = i.ean, kategorie = i.kategorie, gewicht_g = i.gewicht_g
			FROM imp i JOIN angebote an ON an.lieferant_id = $1 AND an.lieferanten_nr = i.lieferanten_nr
			WHERE a.id = an.artikel_id AND a.quelle_lieferant_id = $1
			  AND (a.bezeichnung, a.beschreibung, a.hersteller, a.hersteller_nr, a.ean, a.kategorie, a.gewicht_g)
			      IS DISTINCT FROM (i.bezeichnung, i.beschreibung, i.hersteller, i.hersteller_nr, i.ean, i.kategorie, i.gewicht_g)
			RETURNING a.id),
		  s AS (INSERT INTO suche SELECT id FROM u RETURNING 1)
		SELECT count(*) FROM s`, lieferantID); err != nil {
		return b, err
	}

	// 3. neue Angebote: Artikel suchen oder anlegen. Nur Zeilen ohne bestehendes
	// Angebot – täglich sind das wenige.
	if err := anzahl(nil, `CREATE TEMP TABLE neu ON COMMIT DROP AS
		SELECT i.* FROM imp i
		WHERE NOT EXISTS (SELECT 1 FROM angebote an WHERE an.lieferant_id = $1 AND an.lieferanten_nr = i.lieferanten_nr)`,
		lieferantID); err != nil {
		return b, err
	}
	if err := zaehlen(&b.EANMehrdeutig, `SELECT count(*) FROM neu i
		WHERE i.artikel_id IS NULL AND i.ean <> ''
		  AND (SELECT count(*) FROM artikel a WHERE a.ean = i.ean) > 1`); err != nil {
		return b, err
	}
	var perEAN, perNr int
	if err := anzahl(&perEAN, `UPDATE neu SET artikel_id = m.id
		FROM (SELECT ean, min(id) AS id FROM artikel WHERE ean <> '' GROUP BY ean HAVING count(*) = 1) m
		WHERE neu.artikel_id IS NULL AND neu.ean <> '' AND neu.ean = m.ean`); err != nil {
		return b, err
	}
	if err := anzahl(&perNr, `UPDATE neu SET artikel_id = m.id
		FROM (SELECT lower(hersteller) AS h, lower(hersteller_nr) AS n, min(id) AS id FROM artikel
		      WHERE hersteller_nr <> '' GROUP BY 1, 2 HAVING count(*) = 1) m
		WHERE neu.artikel_id IS NULL AND neu.hersteller_nr <> ''
		  AND lower(neu.hersteller) = m.h AND lower(neu.hersteller_nr) = m.n`); err != nil {
		return b, err
	}
	b.Zugeordnet = perEAN + perNr
	for _, s := range []string{
		`UPDATE neu SET artikel_id = g.id, neu = true
		 FROM (SELECT ean, nextval('artikel_id_seq') AS id
		       FROM (SELECT DISTINCT ean FROM neu WHERE artikel_id IS NULL AND ean <> '') x) g
		 WHERE neu.artikel_id IS NULL AND neu.ean = g.ean`,
		`UPDATE neu SET artikel_id = nextval('artikel_id_seq'), neu = true WHERE artikel_id IS NULL`,
	} {
		if err := anzahl(nil, s); err != nil {
			return b, err
		}
	}
	if err := anzahl(&b.ArtikelNeu, `INSERT INTO artikel (id, bezeichnung, beschreibung, hersteller, hersteller_nr,
			ean, kategorie, gewicht_g, quelle_lieferant_id)
		SELECT DISTINCT ON (artikel_id) artikel_id, bezeichnung, beschreibung, hersteller, hersteller_nr,
			ean, kategorie, gewicht_g, $1
		FROM neu WHERE neu ORDER BY artikel_id, lieferanten_nr`, lieferantID); err != nil {
		return b, err
	}
	if err := protokoll(`SELECT $1, CASE WHEN i.neu THEN 'artikel_neu' ELSE 'zugeordnet' END, i.lieferanten_nr,
			a.nummer, CASE WHEN i.neu THEN i.bezeichnung ELSE a.bezeichnung END, '', i.ek::text
		FROM neu i JOIN artikel a ON a.id = i.artikel_id
		ORDER BY i.artikel_id`, laufID); err != nil {
		return b, err
	}
	if err := zaehlen(&b.AngeboteNeu, `WITH n AS (
			INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand, lieferfrist_tage,
				bild_urls, stand_lieferant)
			SELECT i.artikel_id, $1, i.lieferanten_nr, i.ek, i.lagerbestand, i.lieferfrist_tage,
				coalesce(i.bild_urls, '{}'), $2
			FROM neu i
			ORDER BY i.artikel_id
			RETURNING artikel_id),
		  b AS (INSERT INTO betroffen SELECT artikel_id FROM n RETURNING 1),
		  s AS (INSERT INTO suche SELECT artikel_id FROM n)
		SELECT count(*) FROM b`, lieferantID, stand); err != nil {
		return b, err
	}

	// 4. nicht mehr gelistete Angebote
	if err := protokoll(`SELECT $2, 'entfernt', an.lieferanten_nr, a.nummer, a.bezeichnung, an.ek::text, ''
		FROM angebote an JOIN artikel a ON a.id = an.artikel_id
		WHERE an.lieferant_id = $1 AND an.aktiv
		  AND NOT EXISTS (SELECT 1 FROM imp i WHERE i.lieferanten_nr = an.lieferanten_nr)`, lieferantID, laufID); err != nil {
		return b, err
	}
	if err := zaehlen(&b.Deaktiviert, `WITH w AS (
			UPDATE angebote an SET aktiv = false, geaendert = now()
			WHERE an.lieferant_id = $1 AND an.aktiv
			  AND NOT EXISTS (SELECT 1 FROM imp i WHERE i.lieferanten_nr = an.lieferanten_nr)
			RETURNING an.artikel_id),
		  b AS (INSERT INTO betroffen SELECT artikel_id FROM w RETURNING 1)
		SELECT count(*) FROM b`, lieferantID); err != nil {
		return b, err
	}

	// Suchtext neuer und umbenannter Artikel (Trigger ruhen beim Massenimport)
	if _, err := tx.ExecContext(ctx, `SELECT artikel_suche_auffrischen(array(SELECT DISTINCT artikel_id FROM suche))`); err != nil {
		return b, err
	}

	// 5. aktiv nach Angeboten, neuer Stand für alle betroffenen Artikel
	// Neu angelegte Artikel sind schon aktiv und haben einen frischen Stand –
	// sie nochmals zu schreiben kostete beim ersten Import über 10 s.
	if err := anzahl(nil, `UPDATE artikel a
		SET aktiv = EXISTS (SELECT 1 FROM angebote an WHERE an.artikel_id = a.id AND an.aktiv), geaendert = now()
		WHERE a.id IN (SELECT artikel_id FROM betroffen EXCEPT SELECT artikel_id FROM neu WHERE neu)`); err != nil {
		return b, err
	}

	if err := tx.Commit(); err != nil {
		return b, err
	}
	b.Sekunden = time.Since(start).Round(100 * time.Millisecond).Seconds()
	if b.EANMehrdeutig > 0 {
		b.Hinweise = append(b.Hinweise, fmt.Sprintf("%d Artikel mit einer EAN, die im Katalog mehrfach vorkommt, "+
			"wurden über die Herstellernummer zugeordnet oder neu angelegt.", b.EANMehrdeutig))
	}
	return b, nil
}
