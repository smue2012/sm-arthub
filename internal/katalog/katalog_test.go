package katalog_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/smue2012/sm-arthub/internal/datenbank"
	"github.com/smue2012/sm-arthub/internal/katalog"
	"github.com/smue2012/sm-arthub/internal/schluessel"
)

// testDB legt eine frische Datenbank an. Ohne ARTHUB_TEST_DB wird übersprungen.
// ARTHUB_TEST_DB zeigt auf eine bestehende Datenbank (z.B. postgres), deren
// Benutzer Datenbanken anlegen darf.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("ARTHUB_TEST_DB")
	if url == "" {
		t.Skip("ARTHUB_TEST_DB nicht gesetzt")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := fmt.Sprintf("arthub_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP DATABASE " + name + " WITH (FORCE)") })

	db, err := datenbank.Verbinden(mitDatenbank(url, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := datenbank.Migrieren(db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return db
}

// mitDatenbank ersetzt den Datenbanknamen in einer URL postgres://…/<db>?…
func mitDatenbank(url, name string) string {
	start, ende := -1, len(url)
	for i := len("postgres://"); i < len(url); i++ {
		if url[i] == '/' && start < 0 {
			start = i
		}
		if url[i] == '?' {
			ende = i
			break
		}
	}
	return url[:start+1] + name + url[ende:]
}

func muss(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(q+" RETURNING id", args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return id
}

func TestBestpreis(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	alltron := muss(t, db, `INSERT INTO lieferanten (kuerzel, name) VALUES ('alltron', 'Alltron')`)
	brack := muss(t, db, `INSERT INTO lieferanten (kuerzel, name) VALUES ('brack', 'Brack')`)
	pax8 := muss(t, db, `INSERT INTO lieferanten (kuerzel, name) VALUES ('pax8', 'Pax8')`)

	// 1: Brack günstiger, aber nicht an Lager → Alltron gewinnt
	a1 := muss(t, db, `INSERT INTO artikel (bezeichnung) VALUES ('Maus')`)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand, bild_urls)
		VALUES ($1, $2, 'A-1', 20.50, 3, '{https://bilder.example/a1.jpg}')`, a1, alltron)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand)
		VALUES ($1, $2, 'B-1', 19.90, 0)`, a1, brack)
	// 2: gleicher EK → höherer Lagerbestand gewinnt
	a2 := muss(t, db, `INSERT INTO artikel (bezeichnung) VALUES ('Tastatur')`)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand)
		VALUES ($1, $2, 'A-2', 30, 2)`, a2, alltron)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand)
		VALUES ($1, $2, 'B-2', 30, 15)`, a2, brack)
	// 3: Software, keiner verfügbar → günstigstes, verfuegbar=false
	a3 := muss(t, db, `INSERT INTO artikel (bezeichnung, art) VALUES ('Lizenz', 'software')`)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand)
		VALUES ($1, $2, 'P-3', 8, 0)`, a3, pax8)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand)
		VALUES ($1, $2, 'B-3', 9, 0)`, a3, brack)
	// 4: ohne Angebot
	muss(t, db, `INSERT INTO artikel (bezeichnung) VALUES ('Leer')`)

	r := katalog.Repository{DB: db}
	liste, mehr, err := r.Seit(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if mehr || len(liste) != 4 {
		t.Fatalf("%d artikel, mehr=%v", len(liste), mehr)
	}
	erwartet := map[string]string{"Maus": "alltron", "Tastatur": "brack", "Lizenz": "pax8", "Leer": ""}
	for _, a := range liste {
		got := ""
		if a.Bestes != nil {
			got = a.Bestes.Lieferant
		}
		if got != erwartet[a.Bezeichnung] {
			t.Errorf("%s: bestes %q, erwartet %q", a.Bezeichnung, got, erwartet[a.Bezeichnung])
		}
	}
	if b := liste[0].Bestes; b.EK != "20.5000" || len(b.BildURLs) != 1 || !b.Verfuegbar {
		t.Errorf("Maus: %+v", b)
	}
	if liste[2].Bestes.Verfuegbar || liste[2].Art != "software" {
		t.Errorf("Lizenz: %+v", liste[2])
	}
	if liste[3].Angebote == nil || len(liste[3].Angebote) != 0 {
		t.Errorf("Leer: angebote %v", liste[3].Angebote)
	}
	if liste[0].Nummer != "AH00000018" || liste[1].Nummer != "AH00000026" {
		t.Errorf("nummern %q %q", liste[0].Nummer, liste[1].Nummer)
	}
	for _, a := range liste {
		if !katalog.NummerGueltig(a.Nummer) {
			t.Errorf("Prüfziffer aus der Datenbank passt nicht zu Go: %s", a.Nummer)
		}
	}
}

func TestDeltaAbgleich(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := katalog.Repository{DB: db}
	l := muss(t, db, `INSERT INTO lieferanten (kuerzel, name) VALUES ('alltron', 'Alltron')`)
	a1 := muss(t, db, `INSERT INTO artikel (bezeichnung) VALUES ('Eins')`)
	muss(t, db, `INSERT INTO artikel (bezeichnung) VALUES ('Zwei')`)
	an := muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek)
		VALUES ($1, $2, 'X', 1)`, a1, l)

	// Seitenweise holen
	s1, mehr, err := r.Seit(ctx, 0, 1)
	if err != nil || len(s1) != 1 || !mehr {
		t.Fatalf("seite 1: %d %v %v", len(s1), mehr, err)
	}
	s2, mehr, err := r.Seit(ctx, s1[0].Stand, 1)
	if err != nil || len(s2) != 1 || mehr {
		t.Fatalf("seite 2: %d %v %v", len(s2), mehr, err)
	}
	stand := s2[0].Stand
	if leer, _, _ := r.Seit(ctx, stand, 10); len(leer) != 0 {
		t.Fatalf("nichts geändert, trotzdem %d", len(leer))
	}

	// Angebot ändert sich → Artikel erscheint wieder
	if _, err := db.Exec(`UPDATE angebote SET ek = 2 WHERE id = $1`, an); err != nil {
		t.Fatal(err)
	}
	neu, _, _ := r.Seit(ctx, stand, 10)
	if len(neu) != 1 || neu[0].Bezeichnung != "Eins" || neu[0].Bestes.EK != "2.0000" {
		t.Fatalf("nach EK-Änderung: %+v", neu)
	}

	// Inaktiv gesetzter Artikel kommt mit aktiv=false
	stand = neu[0].Stand
	db.Exec(`UPDATE artikel SET aktiv = false WHERE bezeichnung = 'Zwei'`)
	neu, _, _ = r.Seit(ctx, stand, 10)
	if len(neu) != 1 || neu[0].Aktiv {
		t.Fatalf("inaktiv: %+v", neu)
	}

	// Angebot gelöscht → Artikel ohne Angebot
	stand = neu[0].Stand
	db.Exec(`DELETE FROM angebote WHERE id = $1`, an)
	neu, _, _ = r.Seit(ctx, stand, 10)
	if len(neu) != 1 || neu[0].Bestes != nil {
		t.Fatalf("nach Löschen: %+v", neu)
	}
}

func TestSchluessel(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	v := schluessel.Verwaltung{DB: db}
	s, err := v.Anlegen(ctx, "smerp")
	if err != nil {
		t.Fatal(err)
	}
	if app, err := v.Pruefen(ctx, s); err != nil || app != "smerp" {
		t.Fatalf("prüfen: %q %v", app, err)
	}
	if _, err := v.Pruefen(ctx, s+"x"); err != schluessel.ErrUngueltig {
		t.Fatalf("falscher Schlüssel: %v", err)
	}
	var gespeichert []byte
	db.QueryRow(`SELECT hash FROM api_schluessel WHERE name = 'smerp'`).Scan(&gespeichert)
	if string(gespeichert) == s || len(gespeichert) != 32 {
		t.Fatal("es darf nur der Hash gespeichert sein")
	}
	if err := v.Sperren(ctx, "smerp"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Pruefen(ctx, s); err != schluessel.ErrUngueltig {
		t.Fatalf("gesperrt: %v", err)
	}
	// Neu anlegen ersetzt den alten Schlüssel
	s2, _ := v.Anlegen(ctx, "smerp")
	if _, err := v.Pruefen(ctx, s); err != schluessel.ErrUngueltig {
		t.Fatal("alter Schlüssel gilt noch")
	}
	if _, err := v.Pruefen(ctx, s2); err != nil {
		t.Fatalf("neuer Schlüssel: %v", err)
	}
}

func TestSuche(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r := katalog.Repository{DB: db}
	l := muss(t, db, `INSERT INTO lieferanten (kuerzel, name) VALUES ('alltron', 'Alltron')`)
	maus := muss(t, db, `INSERT INTO artikel (bezeichnung, hersteller, hersteller_nr, ean)
		VALUES ('MX Master 3S Maus', 'Logitech', '910-006559', '5099206103904')`)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand)
		VALUES ($1, $2, 'ALL-4711', 79.9, 5)`, maus, l)
	tast := muss(t, db, `INSERT INTO artikel (bezeichnung, hersteller) VALUES ('MX Keys Tastatur 100%', 'Logitech')`)
	muss(t, db, `INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand)
		VALUES ($1, $2, 'ALL-4712', 99, 0)`, tast, l)
	muss(t, db, `INSERT INTO artikel (bezeichnung, hersteller, aktiv) VALUES ('Alte Maus', 'Logitech', false)`)

	treffer := func(q string, verf bool) []string {
		t.Helper()
		liste, _, err := r.Suche(ctx, q, 10, verf)
		if err != nil {
			t.Fatal(err)
		}
		var namen []string
		for _, a := range liste {
			namen = append(namen, a.Bezeichnung)
		}
		return namen
	}
	if n := treffer("logitech", false); len(n) != 2 {
		t.Errorf("logitech: %v (inaktive dürfen nicht kommen)", n)
	}
	if n := treffer("logitech maus", false); len(n) != 1 || n[0] != "MX Master 3S Maus" {
		t.Errorf("logitech maus: %v", n)
	}
	if n := treffer("ALL-4712", false); len(n) != 1 {
		t.Errorf("lieferanten-nr: %v", n)
	}
	if n := treffer("5099206103904", false); len(n) != 1 {
		t.Errorf("ean: %v", n)
	}
	if n := treffer("logitech", true); len(n) != 1 || n[0] != "MX Master 3S Maus" {
		t.Errorf("nur verfügbar: %v", n)
	}
	if n := treffer("100%", false); len(n) != 1 {
		t.Errorf("prozent wörtlich: %v", n)
	}
	if n := treffer("10_%", false); len(n) != 0 {
		t.Errorf("_ und %% wörtlich: %v", n)
	}
	liste, mehr, _ := r.Suche(ctx, "mx", 1, false)
	if len(liste) != 1 || !mehr || liste[0].Bestes == nil {
		t.Errorf("limit/mehr: %d %v", len(liste), mehr)
	}
}
