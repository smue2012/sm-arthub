// Paket datenbank kümmert sich um die Verbindung zu PostgreSQL und um die
// Migrationen (das schrittweise Anlegen und Ändern der Tabellen).
package datenbank

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq" // PostgreSQL-Treiber, meldet sich bei database/sql an
)

//go:embed migrationen/*.sql
var migrationsDateien embed.FS

// Verbinden öffnet einen Verbindungspool und prüft, ob die Datenbank antwortet.
// Beim Start im Docker kann die Datenbank noch hochfahren – darum bis zu
// 30 Sekunden lang erneut versuchen.
func Verbinden(url string) (*sql.DB, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	var letzterFehler error
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		letzterFehler = db.PingContext(ctx)
		cancel()
		if letzterFehler == nil {
			return db, nil
		}
		time.Sleep(time.Second)
	}
	db.Close()
	return nil, fmt.Errorf("datenbank nach 30 Versuchen nicht erreichbar: %w", letzterFehler)
}

// Migrieren führt alle SQL-Dateien aus «migrationen» aus, die noch nicht
// gelaufen sind (gemerkt in schema_migrationen). Neue Tabellen oder Spalten =
// neue Datei mit der nächsten Nummer. Bestehende Dateien nie ändern.
func Migrieren(db *sql.DB, log *slog.Logger) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrationen (
		version     TEXT PRIMARY KEY,
		ausgefuehrt TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}

	dateien, err := fs.Glob(migrationsDateien, "migrationen/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(dateien)

	for _, datei := range dateien {
		version := strings.TrimSuffix(strings.TrimPrefix(datei, "migrationen/"), ".sql")
		var vorhanden bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM schema_migrationen WHERE version = $1)`,
			version).Scan(&vorhanden); err != nil {
			return err
		}
		if vorhanden {
			continue
		}
		inhalt, err := migrationsDateien.ReadFile(datei)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(inhalt)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrationen (version) VALUES ($1)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		log.Info("migration ausgeführt", "version", version)
	}
	return nil
}

// Stand liefert die Nummer der neuesten Migration (z.B. "001").
func Stand() string {
	dateien, _ := fs.Glob(migrationsDateien, "migrationen/*.sql")
	sort.Strings(dateien)
	if len(dateien) == 0 {
		return ""
	}
	nr, _, _ := strings.Cut(strings.TrimPrefix(dateien[len(dateien)-1], "migrationen/"), "_")
	return nr
}
