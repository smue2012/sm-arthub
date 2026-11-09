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
	Kuerzel         string `json:"kuerzel"`
	Name            string `json:"name"`
	LieferantAktiv  bool   `json:"lieferant_aktiv"`
	Import          bool   `json:"import"` // Import eingerichtet
	Format          string `json:"format"`
	URL             string `json:"url"`
	Benutzer        string `json:"benutzer"`
	PasswortGesetzt bool   `json:"passwort_gesetzt"`
	Fingerabdruck   string `json:"fingerabdruck"`
	BildlisteURL    string `json:"bildliste_url"`
	Zeit            string `json:"zeit"`
	Aktiv           bool   `json:"aktiv"` // Zeitplan an
	Angebote        int    `json:"angebote"`
	LetzterLauf     *Lauf  `json:"letzter_lauf"`

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
			&q.PasswortGesetzt, &q.Fingerabdruck, &q.BildlisteURL, &q.Zeit, &q.Aktiv, &q.Angebote); err != nil {
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
			i.fingerabdruck, i.bildliste_url, i.zeit, i.aktiv
		FROM lieferanten l JOIN importe i ON i.lieferant_id = l.id WHERE l.kuerzel = $1`, kuerzel).
		Scan(&q.id, &q.Kuerzel, &q.Name, &q.Format, &q.URL, &q.Benutzer, &q.passwort,
			&q.Fingerabdruck, &q.BildlisteURL, &q.Zeit, &q.Aktiv)
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
			fingerabdruck, bildliste_url, zeit, aktiv)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (lieferant_id) DO UPDATE SET format = EXCLUDED.format, url = EXCLUDED.url,
			benutzer = EXCLUDED.benutzer,
			passwort = CASE WHEN $10 THEN EXCLUDED.passwort ELSE importe.passwort END,
			fingerabdruck = EXCLUDED.fingerabdruck, bildliste_url = EXCLUDED.bildliste_url,
			zeit = EXCLUDED.zeit, aktiv = EXCLUDED.aktiv`,
		id, e.Format, e.URL, strings.TrimSpace(e.Benutzer), e.Passwort, e.Fingerabdruck, e.BildlisteURL,
		e.Zeit, e.Aktiv, passwortSetzen)
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
	if art != "datei" && q.URL == "" {
		return 0, Fehler("Adresse der Preisliste fehlt")
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

	bericht, stand, err := func() (Bericht, time.Time, error) {
		var stand time.Time
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
		b, err := Abgleichen(ctx, d.DB, q.id, zeilen, bilder, stand)
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
	rows, err := d.DB.QueryContext(ctx, `SELECT l.kuerzel, i.zeit,
			(SELECT max(start) FROM import_laeufe lf WHERE lf.lieferant_id = l.id)
		FROM importe i JOIN lieferanten l ON l.id = i.lieferant_id
		WHERE i.aktiv AND l.aktiv AND i.zeit <> '' AND i.url <> ''`)
	if err != nil {
		if ctx.Err() == nil {
			d.Log.Error("zeitplan", "fehler", err)
		}
		return
	}
	type faellig struct {
		kuerzel string
		zeit    string
		zuletzt *time.Time
	}
	var liste []faellig
	for rows.Next() {
		var f faellig
		if rows.Scan(&f.kuerzel, &f.zeit, &f.zuletzt) == nil {
			liste = append(liste, f)
		}
	}
	rows.Close()
	for _, f := range liste {
		h, m := int(f.zeit[0]-'0')*10+int(f.zeit[1]-'0'), int(f.zeit[3]-'0')*10+int(f.zeit[4]-'0')
		heute := time.Date(jetzt.Year(), jetzt.Month(), jetzt.Day(), h, m, 0, 0, jetzt.Location())
		if jetzt.Before(heute) || (f.zuletzt != nil && !f.zuletzt.Before(heute)) {
			continue
		}
		if _, err := d.Starten(f.kuerzel, "zeitplan", nil); err != nil {
			d.Log.Warn("zeitplan: start nicht möglich", "lieferant", f.kuerzel, "fehler", err)
		}
	}
}
