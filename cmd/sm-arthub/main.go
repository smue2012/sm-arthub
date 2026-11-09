// sm-arthub – Artikelkatalog-Hub: stellt die Handelsware (Waren und
// Softwarelizenzen) der Lieferanten allen internen Applikationen über eine
// Lese-API bereit.
//
// Aufruf:
//
//	sm-arthub                          Server starten (Lese-API und Verwaltung)
//	sm-arthub health                   Healthcheck (für Docker)
//	sm-arthub schluessel anlegen APP   Notfall: API-Schlüssel ohne Oberfläche erzeugen
//	sm-arthub schluessel liste         Schlüssel auflisten (ohne Geheimnis)
//	sm-arthub schluessel sperren APP   Schlüssel von APP sperren
//
// Umgebung:
//
//	DATABASE_URL            Pflicht
//	PORT                    Lese-API für die Apps (Vorgabe 8110, nur im Docker-Netz)
//	ADMIN_PORT              Verwaltungsoberfläche (Vorgabe 8111)
//	ARTHUB_ADMIN_PASSWORT   Passwort der Verwaltung, mindestens 12 Zeichen;
//	                        fehlt es, startet die Verwaltung nicht
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/smue2012/sm-arthub/internal/api"
	"github.com/smue2012/sm-arthub/internal/datenbank"
	"github.com/smue2012/sm-arthub/internal/katalog"
	"github.com/smue2012/sm-arthub/internal/schluessel"
	"github.com/smue2012/sm-arthub/internal/verwaltung"
)

// Von GitHub Actions beim Bauen gesetzt.
var (
	version = "lokal"
	gebaut  = ""
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	port := os.Getenv("PORT")
	if port == "" {
		port = "8110"
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "health":
			os.Exit(health(port))
		case "schluessel":
			os.Exit(schluesselBefehl(log, os.Args[2:]))
		default:
			fmt.Fprintf(os.Stderr, "unbekannter Befehl %q\n", os.Args[1])
			os.Exit(2)
		}
	}

	if err := starten(log, port); err != nil {
		log.Error("abbruch", "fehler", err)
		os.Exit(1)
	}
}

func verbinden(log *slog.Logger) (*datenbankVerbindung, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, errors.New("DATABASE_URL fehlt")
	}
	db, err := datenbank.Verbinden(url)
	if err != nil {
		return nil, err
	}
	if err := datenbank.Migrieren(db, log); err != nil {
		db.Close()
		return nil, err
	}
	return &datenbankVerbindung{db}, nil
}

func starten(log *slog.Logger, port string) error {
	v, err := verbinden(log)
	if err != nil {
		return err
	}
	defer v.Close()

	db := v.DB
	server := []*http.Server{{
		Addr:              ":" + port,
		Handler:           api.Handler(katalog.Repository{DB: db}, schluessel.Verwaltung{DB: db}, log),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      120 * time.Second,
	}}

	adminPort := os.Getenv("ADMIN_PORT")
	if adminPort == "" {
		adminPort = "8111"
	}
	passwort := os.Getenv("ARTHUB_ADMIN_PASSWORT")
	switch {
	case passwort == "":
		log.Warn("ARTHUB_ADMIN_PASSWORT fehlt – Verwaltungsoberfläche ist aus")
	case len(passwort) < 12:
		return errors.New("ARTHUB_ADMIN_PASSWORT muss mindestens 12 Zeichen haben")
	case adminPort == port:
		return errors.New("ADMIN_PORT und PORT müssen verschieden sein")
	default:
		server = append(server, &http.Server{
			Addr: ":" + adminPort,
			Handler: verwaltung.Handler(verwaltung.Optionen{
				Passwort:         passwort,
				Schluessel:       schluessel.Verwaltung{DB: db},
				Status:           statusQuelle(db),
				Log:              log,
				FehlVerzoegerung: time.Second,
			}),
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      30 * time.Second,
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fehler := make(chan error, len(server))
	for _, srv := range server {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fehler <- fmt.Errorf("%s: %w", srv.Addr, err)
				return
			}
			fehler <- nil
		}()
	}
	log.Info("sm-arthub gestartet", "port", port, "admin_port", adminPort,
		"verwaltung", len(server) > 1, "version", version, "gebaut", gebaut,
		"migration", datenbank.Stand())

	var ergebnis error
	select {
	case <-ctx.Done():
	case ergebnis = <-fehler:
	}
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range server {
		srv.Shutdown(c)
	}
	return ergebnis
}

// statusQuelle zählt Artikel, Angebote und Lieferanten für die Verwaltung.
func statusQuelle(db *sql.DB) verwaltung.StatusQuelle {
	return func(ctx context.Context) (verwaltung.Status, error) {
		st := verwaltung.Status{Version: version, Migration: datenbank.Stand()}
		err := db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM artikel WHERE aktiv),
			(SELECT count(*) FROM angebote WHERE aktiv),
			(SELECT count(*) FROM lieferanten WHERE aktiv)`).
			Scan(&st.Artikel, &st.Angebote, &st.Lieferanten)
		return st, err
	}
}

func health(port string) int {
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func schluesselBefehl(log *slog.Logger, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "sm-arthub schluessel anlegen|liste|sperren [APP]")
		return 2
	}
	v, err := verbinden(log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer v.Close()
	verw := schluessel.Verwaltung{DB: v.DB}
	ctx := context.Background()

	switch args[0] {
	case "anlegen":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "sm-arthub schluessel anlegen APP")
			return 2
		}
		s, err := verw.Anlegen(ctx, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Schlüssel für %s (wird nur jetzt angezeigt):\n%s\n", args[1], s)
	case "sperren":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "sm-arthub schluessel sperren APP")
			return 2
		}
		if err := verw.Sperren(ctx, args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Schlüssel von %s gesperrt.\n", args[1])
	case "liste":
		liste, err := verw.Liste(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, e := range liste {
			status, zuletzt := "aktiv", "nie"
			if !e.Aktiv {
				status = "gesperrt"
			}
			if e.Zuletzt != nil {
				zuletzt = e.Zuletzt.Local().Format("02.01.2006 15:04")
			}
			fmt.Printf("%-20s %-9s erstellt %s, zuletzt %s\n", e.Name, status,
				e.Erstellt.Local().Format("02.01.2006"), zuletzt)
		}
	default:
		fmt.Fprintf(os.Stderr, "unbekannt: %q\n", args[0])
		return 2
	}
	return 0
}

type datenbankVerbindung struct {
	*sql.DB
}
