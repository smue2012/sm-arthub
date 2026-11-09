// Paket importer holt die Preislisten der Lieferanten und gleicht sie mit dem
// Katalog ab: von Hand («Jetzt importieren», Datei hochladen) oder täglich
// nach Zeitplan. Jeder Lauf wird in import_laeufe protokolliert.
//
// Zugangsdaten (Passwort) liegen nur in der Datenbank des Hubs und werden nie
// an die Oberfläche zurückgegeben.
package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Fehler: Meldung für die Oberfläche (im Gegensatz zu internen Fehlern).
type Fehler string

func (f Fehler) Error() string { return string(f) }

// Formate der Preislisten.
var Formate = map[string]string{"competec": "Alltron / Competec (ArtikelPreisdatenV2.csv)"}

// Quelle: ein Lieferant mit seinem Import, wie ihn die Verwaltung sieht.
type Quelle struct {
	Kuerzel          string `json:"kuerzel"`
	Name             string `json:"name"`
	LieferantAktiv   bool   `json:"lieferant_aktiv"`
	Import           bool   `json:"import"` // Import eingerichtet
	Format           string `json:"format"`
	URL              string `json:"url"`
	Benutzer         string `json:"benutzer"`
	PasswortGesetzt  bool   `json:"passwort_gesetzt"`
	Fingerabdruck    string `json:"fingerabdruck"`
	BildlisteURL     string `json:"bildliste_url"`
	Zeit             string `json:"zeit"`
	Aktiv            bool   `json:"aktiv"` // Zeitplan an
	BestandURL       string `json:"bestand_url"`
	BestandIntervall int    `json:"bestand_intervall"` // Minuten, 0 = aus
	BestandVon       string `json:"bestand_von"`
	BestandBis       string `json:"bestand_bis"`
	Angebote         int    `json:"angebote"`
	LetzterLauf      *Lauf  `json:"letzter_lauf"`

	id       int
	passwort string
}

// Eingabe beim Speichern eines Imports. Passwort leer = unverändert.
type Eingabe struct {
	Format           string `json:"format"`
	URL              string `json:"url"`
	Benutzer         string `json:"benutzer"`
	Passwort         string `json:"passwort"`
	PasswortLoeschen bool   `json:"passwort_loeschen"`
	Fingerabdruck    string `json:"fingerabdruck"`
	BildlisteURL     string `json:"bildliste_url"`
	Zeit             string `json:"zeit"`
	Aktiv            bool   `json:"aktiv"`
	BestandURL       string `json:"bestand_url"`
	BestandIntervall int    `json:"bestand_intervall"`
	BestandVon       string `json:"bestand_von"`
	BestandBis       string `json:"bestand_bis"`
}

// Lauf: ein Import mit Ergebnis.
type Lauf struct {
	ID         int64      `json:"id"`
	Lieferant  string     `json:"lieferant"`
	Art        string     `json:"art"`
	Status     string     `json:"status"`
	Schritt    string     `json:"schritt"`
	Start      time.Time  `json:"start"`
	Ende       *time.Time `json:"ende"`
	DateiStand *time.Time `json:"datei_stand"`
	Bericht    *Bericht   `json:"bericht"`
	Meldung    string     `json:"meldung"`
}

// Dienst führt die Importe aus.
type Dienst struct {
	DB  *sql.DB
	Log *slog.Logger

	ctx     context.Context
	mu      sync.Mutex
	laufend map[int]bool
}

// Neu: ctx beendet laufende Importe beim Herunterfahren. Läufe, die bei einem
// Neustart unterbrochen wurden, werden als Fehler abgeschlossen.
func Neu(ctx context.Context, db *sql.DB, log *slog.Logger) *Dienst {
	if _, err := db.ExecContext(ctx, `UPDATE import_laeufe SET status = 'fehler', ende = now(),
		meldung = 'Abgebrochen (Neustart des Hubs)' WHERE status = 'laeuft'`); err != nil {
		log.Error("unterbrochene läufe abschliessen", "fehler", err)
	}
	return &Dienst{DB: db, Log: log, ctx: ctx, laufend: map[int]bool{}}
}

var kuerzelMuster = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,29}$`)
var zeitMuster = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// Lieferanten liefert alle Lieferanten mit Import und letztem Lauf.
func (d *Dienst) Lieferanten(ctx context.Context) ([]Quelle, error) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT l.id, l.kuerzel, l.name, l.aktiv, i.lieferant_id IS NOT NULL,
		       coalesce(i.format, ''), coalesce(i.url, ''), coalesce(i.benutzer, ''), coalesce(i.passwort, '') <> '',
		       coalesce(i.fingerabdruck, ''), coalesce(i.bildliste_url, ''), coalesce(i.zeit, ''), coalesce(i.aktiv, false),
		       coalesce(i.bestand_url, ''), coalesce(i.bestand_intervall, 0), coalesce(i.bestand_von, '07:00'),
		       coalesce(i.bestand_bis, '19:00'),
		       (SELECT count(*) FROM angebote an WHERE an.lieferant_id = l.id AND an.aktiv)
		FROM lieferanten l LEFT JOIN importe i ON i.lieferant_id = l.id
		ORDER BY l.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	liste := []Quelle{}
	for rows.Next() {
		var q Quelle
		if err := rows.Scan(&q.id, &q.Kuerzel, &q.Name, &q.LieferantAktiv, &q.Import, &q.Format, &q.URL, &q.Benutzer,
			&q.PasswortGesetzt, &q.Fingerabdruck, &q.BildlisteURL, &q.Zeit, &q.Aktiv,
			&q.BestandURL, &q.BestandIntervall, &q.BestandVon, &q.BestandBis, &q.Angebote); err != nil {
			return nil, err
		}
		liste = append(liste, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range liste {
		laeufe, err := d.laeufe(ctx, liste[i].id, 1)
		if err != nil {
			return nil, err
		}
		if len(laeufe) > 0 {
			liste[i].LetzterLauf = &laeufe[0]
		}
	}
	return liste, nil
}

// LieferantAnlegen legt einen Lieferanten an, optional mit Importformat.
func (d *Dienst) LieferantAnlegen(ctx context.Context, kuerzel, name, format string) error {
	kuerzel, name = strings.TrimSpace(strings.ToLower(kuerzel)), strings.TrimSpace(name)
	if !kuerzelMuster.MatchString(kuerzel) {
		return Fehler("Kürzel: Kleinbuchstaben, Ziffern und Bindestrich, höchstens 30 Zeichen (z.B. alltron)")
	}
	if name == "" || len(name) > 100 {
		return Fehler("Name fehlt")
	}
	if _, ok := Formate[format]; format != "" && !ok {
		return Fehler("Unbekanntes Importformat")
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int
	err = tx.QueryRowContext(ctx, `INSERT INTO lieferanten (kuerzel, name) VALUES ($1, $2)
		ON CONFLICT (kuerzel) DO NOTHING RETURNING id`, kuerzel, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Fehler("Kürzel «" + kuerzel + "» ist schon vergeben")
	}
	if err != nil {
		return err
	}
	if format != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO importe (lieferant_id, format) VALUES ($1, $2)`, id, format); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *Dienst) quelle(ctx context.Context, kuerzel string) (Quelle, error) {
	var q Quelle
	err := d.DB.QueryRowContext(ctx, `SELECT l.id, l.kuerzel, l.name, i.format, i.url, i.benutzer, i.passwort,
			i.fingerabdruck, i.bildliste_url, i.zeit, i.aktiv, i.bestand_url, i.bestand_intervall, i.bestand_von, i.bestand_bis
		FROM lieferanten l JOIN importe i ON i.lieferant_id = l.id WHERE l.kuerzel = $1`, kuerzel).
		Scan(&q.id, &q.Kuerzel, &q.Name, &q.Format, &q.URL, &q.Benutzer, &q.passwort,
			&q.Fingerabdruck, &q.BildlisteURL, &q.Zeit, &q.Aktiv, &q.BestandURL, &q.BestandIntervall, &q.BestandVon, &q.BestandBis)
	if errors.Is(err, sql.ErrNoRows) {
		return q, Fehler("Für «" + kuerzel + "» ist kein Import eingerichtet")
	}
	q.Import = true
	return q, err
}

func adresseGueltig(a string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	for _, p := range []string{"sftp://", "ftps://", "ftp://", "https://"} {
		if strings.HasPrefix(a, p) && len(a) > len(p) {
			return true
		}
	}
	return false
}

// ImportSpeichern legt den Import eines Lieferanten an oder ändert ihn.
func (d *Dienst) ImportSpeichern(ctx context.Context, kuerzel string, e Eingabe) error {
	e.URL, e.BildlisteURL, e.Zeit = strings.TrimSpace(e.URL), strings.TrimSpace(e.BildlisteURL), strings.TrimSpace(e.Zeit)
	e.Fingerabdruck = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(e.Fingerabdruck), "SHA256:"), "=")
	if _, ok := Formate[e.Format]; !ok {
		return Fehler("Unbekanntes Importformat")
	}
	if e.URL != "" && !adresseGueltig(e.URL) {
		return Fehler("Adresse muss mit sftp://, ftps://, ftp:// oder https:// beginnen")
	}
	if e.BildlisteURL != "" && !adresseGueltig(e.BildlisteURL) {
		return Fehler("Adresse der Bildliste muss mit sftp://, ftps://, ftp:// oder https:// beginnen")
	}
	if e.Zeit != "" && !zeitMuster.MatchString(e.Zeit) {
		return Fehler("Zeit im Format HH:MM, z.B. 05:15")
	}
	if e.Aktiv && (e.Zeit == "" || e.URL == "") {
		return Fehler("Für den Zeitplan braucht es Adresse und Zeit")
	}
	e.BestandURL = strings.TrimSpace(e.BestandURL)
	if e.BestandURL != "" && !adresseGueltig(e.BestandURL) {
		return Fehler("Adresse der Lagerbestandsliste muss mit sftp://, ftps://, ftp:// oder https:// beginnen")
	}
	if e.BestandVon == "" {
		e.BestandVon = "07:00"
	}
	if e.BestandBis == "" {
		e.BestandBis = "19:00"
	}
	if !zeitMuster.MatchString(e.BestandVon) || !zeitMuster.MatchString(e.BestandBis) || e.BestandVon > e.BestandBis {
		return Fehler("Lagerbestand: Zeitfenster im Format HH:MM, «von» vor «bis»")
	}
	if e.BestandIntervall != 0 && (e.BestandIntervall < 15 || e.BestandIntervall > 1440) {
		return Fehler("Lagerbestand: Intervall zwischen 15 und 1440 Minuten (0 = aus)")
	}
	if e.BestandIntervall > 0 && e.BestandURL == "" {
		return Fehler("Für den Lagerbestand-Zeitplan braucht es die Adresse der Lagerbestandsliste")
	}
	if len(e.Benutzer) > 200 || len(e.Passwort) > 500 || len(e.URL) > 500 || len(e.BildlisteURL) > 500 {
		return Fehler("Eingabe zu lang")
	}
	var id int
	err := d.DB.QueryRowContext(ctx, `SELECT id FROM lieferanten WHERE kuerzel = $1`, kuerzel).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Fehler("Lieferant nicht gefunden")
	}
	if err != nil {
		return err
	}
	passwortSetzen := e.Passwort != "" || e.PasswortLoeschen
	_, err = d.DB.ExecContext(ctx, `INSERT INTO importe (lieferant_id, format, url, benutzer, passwort,
			fingerabdruck, bildliste_url, zeit, aktiv, bestand_url, bestand_intervall, bestand_von, bestand_bis)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $11, $12, $13, $14)
		ON CONFLICT (lieferant_id) DO UPDATE SET format = EXCLUDED.format, url = EXCLUDED.url,
			benutzer = EXCLUDED.benutzer,
			passwort = CASE WHEN $10 THEN EXCLUDED.passwort ELSE importe.passwort END,
			fingerabdruck = EXCLUDED.fingerabdruck, bildliste_url = EXCLUDED.bildliste_url,
			zeit = EXCLUDED.zeit, aktiv = EXCLUDED.aktiv, bestand_url = EXCLUDED.bestand_url,
			bestand_intervall = EXCLUDED.bestand_intervall, bestand_von = EXCLUDED.bestand_von,
			bestand_bis = EXCLUDED.bestand_bis`,
		id, e.Format, e.URL, strings.TrimSpace(e.Benutzer), e.Passwort, e.Fingerabdruck, e.BildlisteURL,
		e.Zeit, e.Aktiv, passwortSetzen, e.BestandURL, e.BestandIntervall, e.BestandVon, e.BestandBis)
	return err
}

// Fingerabdruck fragt den Hostschlüssel eines SFTP-Servers ab.
func (d *Dienst) Fingerabdruck(ctx context.Context, adresse string) (string, string, error) {
	return Fingerabdruck(ctx, adresse)
}

// Starten beginnt einen Lauf im Hintergrund (art: abruf, datei, zeitplan).
// Bei «datei» wird die mitgegebene Datei statt eines Abrufs verwendet.
func (d *Dienst) Starten(kuerzel, art string, datei []byte) (int64, error) {
	ctx := d.ctx
	q, err := d.quelle(ctx, kuerzel)
	if err != nil {
		return 0, err
	}
	bestandLauf := art == "bestand" || art == "bestand_zeitplan"
	if bestandLauf && q.BestandURL == "" {
		return 0, Fehler("Adresse der Lagerbestandsliste fehlt")
	}
	if !bestandLauf && art != "datei" && q.URL == "" {
		return 0, Fehler("Adresse der Preisliste fehlt")
	}
	if art == "datei" && IstBestandsliste(datei) {
		art = "bestand"
	}
	d.mu.Lock()
	if d.laufend[q.id] {
		d.mu.Unlock()
		return 0, Fehler("Für «" + q.Name + "» läuft schon ein Import")
	}
	d.laufend[q.id] = true
	d.mu.Unlock()

	var id int64
	if err := d.DB.QueryRowContext(ctx, `INSERT INTO import_laeufe (lieferant_id, art, schritt)
		VALUES ($1, $2, 'gestartet') RETURNING id`, q.id, art).Scan(&id); err != nil {
		d.fertig(q.id)
		return 0, err
	}
	go d.ausfuehren(q, id, art, datei)
	return id, nil
}

func (d *Dienst) fertig(lieferantID int) {
	d.mu.Lock()
	delete(d.laufend, lieferantID)
	d.mu.Unlock()
}

func (d *Dienst) schritt(id int64, text string) {
	d.DB.ExecContext(d.ctx, `UPDATE import_laeufe SET schritt = $2 WHERE id = $1`, id, text)
}

func (d *Dienst) ausfuehren(q Quelle, id int64, art string, datei []byte) {
	defer d.fertig(q.id)
	ctx := d.ctx
	log := d.Log.With("lieferant", q.Kuerzel, "lauf", id)
	log.Info("import gestartet", "art", art)
	// Protokoll-Details nach 90 Tagen löschen (die Zusammenfassung bleibt)
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM import_details WHERE lauf_id IN
		(SELECT id FROM import_laeufe WHERE start < now() - interval '90 days')`); err != nil {
		log.Warn("alte protokolle löschen", "fehler", err)
	}

	bericht, stand, err := func() (Bericht, time.Time, error) {
		var stand time.Time
		if art == "bestand" || art == "bestand_zeitplan" {
			if datei == nil {
				d.schritt(id, "Lagerbestandsliste holen")
				var err error
				datei, stand, err = Holen(ctx, q.BestandURL, q.Benutzer, q.passwort, q.Fingerabdruck)
				if err != nil {
					return Bericht{}, stand, err
				}
			}
			d.schritt(id, "Lagerbestand abgleichen")
			bestand, err := BestandLesen(datei)
			datei = nil
			if err != nil {
				return Bericht{}, stand, err
			}
			b, err := BestandAbgleichen(ctx, d.DB, q.id, bestand)
			return b, stand, err
		}
		if datei == nil {
			d.schritt(id, "Preisliste holen")
			var err error
			datei, stand, err = Holen(ctx, q.URL, q.Benutzer, q.passwort, q.Fingerabdruck)
			if err != nil {
				return Bericht{}, stand, err
			}
		}
		d.schritt(id, "Preisliste lesen")
		zeilen, uebersprungen, err := CompetecLesen(datei)
		datei = nil // Speicher freigeben
		if err != nil {
			return Bericht{}, stand, err
		}
		var bilder map[string][]string
		var hinweise []string
		if q.BildlisteURL != "" && art != "datei" {
			d.schritt(id, "Bildliste holen")
			roh, _, err := Holen(ctx, q.BildlisteURL, q.Benutzer, q.passwort, q.Fingerabdruck)
			if err == nil {
				bilder, err = BildlisteLesen(roh)
			}
			if err != nil {
				hinweise = append(hinweise, "Bildliste übersprungen: "+err.Error())
			}
		}
		d.schritt(id, "Abgleich mit dem Katalog")
		b, err := AbgleichenMitProtokoll(ctx, d.DB, q.id, id, zeilen, bilder, stand)
		b.Uebersprungen = uebersprungen
		b.Hinweise = append(hinweise, b.Hinweise...)
		return b, stand, err
	}()

	var dateiStand any
	if !stand.IsZero() {
		dateiStand = stand
	}
	if err != nil {
		meldung := "Interner Fehler – siehe Log des Hubs"
		var f Fehler
		if errors.As(err, &f) {
			meldung = string(f)
		}
		log.Error("import fehlgeschlagen", "fehler", err)
		d.DB.ExecContext(context.Background(), `UPDATE import_laeufe SET status = 'fehler', ende = now(),
			meldung = $2, datei_stand = $3 WHERE id = $1`, id, meldung, dateiStand)
		return
	}
	roh, _ := json.Marshal(bericht)
	d.DB.ExecContext(context.Background(), `UPDATE import_laeufe SET status = 'ok', ende = now(), schritt = '',
		bericht = $2, datei_stand = $3 WHERE id = $1`, id, roh, dateiStand)
	log.Info("import fertig", "zeilen", bericht.Zeilen, "artikel_neu", bericht.ArtikelNeu,
		"angebote_neu", bericht.AngeboteNeu, "geaendert", bericht.AngeboteGeaendert,
		"deaktiviert", bericht.Deaktiviert, "sekunden", bericht.Sekunden)
}

func (d *Dienst) laeufe(ctx context.Context, lieferantID int, limit int) ([]Lauf, error) {
	rows, err := d.DB.QueryContext(ctx, `SELECT lf.id, l.kuerzel, lf.art, lf.status, lf.schritt, lf.start, lf.ende,
			lf.datei_stand, lf.bericht, lf.meldung
		FROM import_laeufe lf JOIN lieferanten l ON l.id = lf.lieferant_id
		WHERE ($1 = 0 OR lf.lieferant_id = $1) ORDER BY lf.start DESC, lf.id DESC LIMIT $2`, lieferantID, limit)
	if err != nil {
		return nil, err
	}
	return laeufeLesen(rows)
}

func laeufeLesen(rows *sql.Rows) ([]Lauf, error) {
	defer rows.Close()
	liste := []Lauf{}
	for rows.Next() {
		var l Lauf
		var roh []byte
		if err := rows.Scan(&l.ID, &l.Lieferant, &l.Art, &l.Status, &l.Schritt, &l.Start, &l.Ende,
			&l.DateiStand, &roh, &l.Meldung); err != nil {
			return nil, err
		}
		if len(roh) > 0 {
			var b Bericht
			if json.Unmarshal(roh, &b) == nil {
				l.Bericht = &b
			}
		}
		liste = append(liste, l)
	}
	return liste, rows.Err()
}

// Detail: eine Zeile des Protokolls eines Laufs.
type Detail struct {
	Art           string `json:"art"`
	LieferantenNr string `json:"lieferanten_nr"`
	ArtikelNummer string `json:"artikel_nummer"`
	Bezeichnung   string `json:"bezeichnung"`
	Alt           string `json:"alt"`
	Neu           string `json:"neu"`
}

// DetailArten in der Reihenfolge der Anzeige, mit Bezeichnung.
var DetailArten = []struct{ Art, Text string }{
	{"artikel_neu", "Neue Artikel"}, {"zugeordnet", "Bestehenden Artikeln zugeordnet"},
	{"preis", "EK geändert"}, {"stammdaten", "Stammdaten geändert"},
	{"entfernt", "Nicht mehr gelistet"}, {"wieder", "Wieder gelistet"},
}

// Details liefert das Protokoll eines Laufs: Anzahl je Art und die Zeilen einer
// Art (art leer = alle), höchstens limit (0 = alle).
func (d *Dienst) Details(ctx context.Context, laufID int64, art string, limit int) (map[string]int, []Detail, error) {
	anzahl := map[string]int{}
	rows, err := d.DB.QueryContext(ctx, `SELECT art, count(*) FROM import_details WHERE lauf_id = $1 GROUP BY art`, laufID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var a string
		var n int
		if err := rows.Scan(&a, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		anzahl[a] = n
	}
	rows.Close()
	q := `SELECT art, lieferanten_nr, artikel_nummer, bezeichnung, alt, neu FROM import_details
		WHERE lauf_id = $1 AND ($2 = '' OR art = $2)
		ORDER BY CASE art WHEN 'artikel_neu' THEN 1 WHEN 'zugeordnet' THEN 2 WHEN 'preis' THEN 3
			WHEN 'stammdaten' THEN 4 WHEN 'entfernt' THEN 5 ELSE 6 END, bezeichnung, lieferanten_nr`
	args := []any{laufID, art}
	if limit > 0 {
		q += ` LIMIT $3`
		args = append(args, limit)
	}
	rows, err = d.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	liste := []Detail{}
	for rows.Next() {
		var x Detail
		if err := rows.Scan(&x.Art, &x.LieferantenNr, &x.ArtikelNummer, &x.Bezeichnung, &x.Alt, &x.Neu); err != nil {
			return nil, nil, err
		}
		liste = append(liste, x)
	}
	return anzahl, liste, rows.Err()
}

// Lauf liefert einen Lauf.
func (d *Dienst) Lauf(ctx context.Context, id int64) (Lauf, error) {
	rows, err := d.DB.QueryContext(ctx, `SELECT lf.id, l.kuerzel, lf.art, lf.status, lf.schritt, lf.start, lf.ende,
			lf.datei_stand, lf.bericht, lf.meldung
		FROM import_laeufe lf JOIN lieferanten l ON l.id = lf.lieferant_id WHERE lf.id = $1`, id)
	if err != nil {
		return Lauf{}, err
	}
	liste, err := laeufeLesen(rows)
	if err != nil {
		return Lauf{}, err
	}
	if len(liste) == 0 {
		return Lauf{}, Fehler("Import nicht gefunden")
	}
	return liste[0], nil
}

// Laeufe liefert die letzten Läufe (kuerzel leer = alle Lieferanten).
func (d *Dienst) Laeufe(ctx context.Context, kuerzel string, limit int) ([]Lauf, error) {
	id := 0
	if kuerzel != "" {
		if err := d.DB.QueryRowContext(ctx, `SELECT id FROM lieferanten WHERE kuerzel = $1`, kuerzel).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []Lauf{}, nil
			}
			return nil, err
		}
	}
	return d.laeufe(ctx, id, limit)
}

// Zeitplan startet jede Minute die fälligen Importe: Zeitplan an, Uhrzeit
// erreicht und heute seit dieser Uhrzeit noch kein Lauf. Läuft bis ctx endet.
func (d *Dienst) Zeitplan(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		d.faelligeStarten(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Dienst) faelligeStarten(ctx context.Context, jetzt time.Time) {
	rows, err := d.DB.QueryContext(ctx, `SELECT l.kuerzel, i.zeit, i.aktiv AND i.zeit <> '' AND i.url <> '',
			i.bestand_intervall, i.bestand_von, i.bestand_bis, i.bestand_url <> '',
			(SELECT max(start) FROM import_laeufe lf WHERE lf.lieferant_id = l.id AND lf.art IN ('abruf', 'datei', 'zeitplan')),
			(SELECT max(start) FROM import_laeufe lf WHERE lf.lieferant_id = l.id AND lf.art IN ('bestand', 'bestand_zeitplan'))
		FROM importe i JOIN lieferanten l ON l.id = i.lieferant_id
		WHERE l.aktiv AND ((i.aktiv AND i.zeit <> '' AND i.url <> '') OR (i.bestand_intervall > 0 AND i.bestand_url <> ''))`)
	if err != nil {
		if ctx.Err() == nil {
			d.Log.Error("zeitplan", "fehler", err)
		}
		return
	}
	type faellig struct {
		kuerzel, zeit, von, bis     string
		voll, bestandAn             bool
		intervall                   int
		zuletztVoll, zuletztBestand *time.Time
	}
	var liste []faellig
	for rows.Next() {
		var f faellig
		if rows.Scan(&f.kuerzel, &f.zeit, &f.voll, &f.intervall, &f.von, &f.bis, &f.bestandAn,
			&f.zuletztVoll, &f.zuletztBestand) == nil {
			liste = append(liste, f)
		}
	}
	rows.Close()
	uhr := func(hhmm string) time.Time {
		h, m := int(hhmm[0]-'0')*10+int(hhmm[1]-'0'), int(hhmm[3]-'0')*10+int(hhmm[4]-'0')
		return time.Date(jetzt.Year(), jetzt.Month(), jetzt.Day(), h, m, 0, 0, jetzt.Location())
	}
	for _, f := range liste {
		// Preisliste: einmal täglich ab der Uhrzeit
		if f.voll && len(f.zeit) == 5 {
			heute := uhr(f.zeit)
			if !jetzt.Before(heute) && (f.zuletztVoll == nil || f.zuletztVoll.Before(heute)) {
				if _, err := d.Starten(f.kuerzel, "zeitplan", nil); err != nil {
					d.Log.Warn("zeitplan: start nicht möglich", "lieferant", f.kuerzel, "fehler", err)
				}
				continue // Bestand frühestens in der nächsten Minute
			}
		}
		// Lagerbestand: im Zeitfenster alle N Minuten
		if f.bestandAn && f.intervall > 0 {
			von, bis := uhr(f.von), uhr(f.bis).Add(time.Minute)
			if jetzt.Before(von) || !jetzt.Before(bis) {
				continue
			}
			if f.zuletztBestand != nil && jetzt.Sub(*f.zuletztBestand) < time.Duration(f.intervall)*time.Minute-30*time.Second {
				continue
			}
			if _, err := d.Starten(f.kuerzel, "bestand_zeitplan", nil); err != nil {
				d.Log.Debug("zeitplan bestand: start nicht möglich", "lieferant", f.kuerzel, "fehler", err)
			}
		}
	}
}
