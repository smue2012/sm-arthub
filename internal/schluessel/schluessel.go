// Paket schluessel verwaltet die API-Schlüssel der Applikationen.
//
// Jede App (smerp, smshop, …) bekommt einen eigenen Schlüssel. In der
// Datenbank liegt nur der SHA-256-Hash; der Schlüssel wird beim Anlegen genau
// einmal ausgegeben und gehört danach nur in die Konfiguration der App
// (serverseitig, nie in einen Browser).
package schluessel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Praefix macht Schlüssel in Logs und Konfigurationen erkennbar.
const Praefix = "ah_"

// ErrUngueltig: Schlüssel unbekannt oder gesperrt.
var ErrUngueltig = errors.New("api-schlüssel ungültig")

// Eintrag für die Liste.
type Eintrag struct {
	Name     string     `json:"name"`
	Aktiv    bool       `json:"aktiv"`
	Erstellt time.Time  `json:"erstellt"`
	Zuletzt  *time.Time `json:"zuletzt"`
}

// Verwaltung arbeitet auf der Tabelle api_schluessel.
type Verwaltung struct {
	DB *sql.DB
}

func hash(schluessel string) []byte {
	h := sha256.Sum256([]byte(schluessel))
	return h[:]
}

// Erzeugen liefert einen neuen zufälligen Schlüssel (256 Bit).
func Erzeugen() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return Praefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Anlegen erzeugt einen Schlüssel für die App name und gibt ihn zurück.
// Gibt es die App schon, wird ihr Schlüssel ersetzt (und wieder aktiv).
func (v Verwaltung) Anlegen(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name der applikation fehlt")
	}
	s, err := Erzeugen()
	if err != nil {
		return "", err
	}
	_, err = v.DB.ExecContext(ctx, `INSERT INTO api_schluessel (name, hash) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET hash = EXCLUDED.hash, aktiv = true, erstellt = now(), zuletzt = NULL`,
		name, hash(s))
	if err != nil {
		return "", err
	}
	return s, nil
}

// Sperren deaktiviert den Schlüssel der App name.
func (v Verwaltung) Sperren(ctx context.Context, name string) error {
	res, err := v.DB.ExecContext(ctx, `UPDATE api_schluessel SET aktiv = false WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("keine applikation %q", name)
	}
	return nil
}

// Liste aller Schlüssel (ohne Geheimnis).
func (v Verwaltung) Liste(ctx context.Context) ([]Eintrag, error) {
	rows, err := v.DB.QueryContext(ctx, `SELECT name, aktiv, erstellt, zuletzt FROM api_schluessel ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var liste []Eintrag
	for rows.Next() {
		var e Eintrag
		if err := rows.Scan(&e.Name, &e.Aktiv, &e.Erstellt, &e.Zuletzt); err != nil {
			return nil, err
		}
		liste = append(liste, e)
	}
	return liste, rows.Err()
}

// Pruefen liefert den Namen der App zum Schlüssel. Verglichen wird über den
// Hash; «zuletzt» wird höchstens einmal pro Minute nachgeführt.
func (v Verwaltung) Pruefen(ctx context.Context, schluessel string) (string, error) {
	if !strings.HasPrefix(schluessel, Praefix) {
		return "", ErrUngueltig
	}
	var name string
	err := v.DB.QueryRowContext(ctx, `UPDATE api_schluessel
		SET zuletzt = CASE WHEN zuletzt IS NULL OR zuletzt < now() - interval '1 minute' THEN now() ELSE zuletzt END
		WHERE hash = $1 AND aktiv RETURNING name`, hash(schluessel)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUngueltig
	}
	return name, err
}
