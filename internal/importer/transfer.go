package importer

// Preislisten holen (übernommen aus smerp internal/katalog/transfer.go).
//
// https holt Go selbst. Für sftp, ftps und ftp ruft der Hub curl auf. Die
// Zugangsdaten gehen über die Standardeingabe (curl --config -), damit sie nicht
// in der Prozessliste stehen.
//
// sftp: Beim Einrichten wird der Fingerabdruck des Hostschlüssels angezeigt und
// bestätigt. Bei jeder Übertragung holt der Hub die Hostschlüssel neu, behält
// nur den mit dem gespeicherten Fingerabdruck und legt ihn curl als
// known_hosts vor (HOME in einem Wegwerfverzeichnis). curl in Ubuntu nutzt
// libssh, das «hostpubsha256» nicht kennt – known_hosts geht mit beiden.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxDatei: grösste Preisliste (die Competec-Liste hat rund 110 MB)
const MaxDatei = 300 << 20

// Befehle lassen sich für Tests ersetzen.
var (
	curlBefehl    = "curl"
	keyscanBefehl = "ssh-keyscan"
)

func init() {
	if c := os.Getenv("ARTHUB_CURL"); c != "" {
		curlBefehl = c
	}
	if c := os.Getenv("ARTHUB_SSH_KEYSCAN"); c != "" {
		keyscanBefehl = c
	}
}

// Holen lädt eine Datei. Zurück kommt auch ihr Datum auf dem Server
// (Null-Zeit = unbekannt).
func Holen(ctx context.Context, adresse, benutzer, passwort, fingerabdruck string) ([]byte, time.Time, error) {
	u, err := url.Parse(strings.TrimSpace(adresse))
	if err != nil || u.Host == "" {
		return nil, time.Time{}, Fehler("Adresse nicht verwendbar: " + adresse)
	}
	ctx, abbrechen := context.WithTimeout(ctx, 30*time.Minute)
	defer abbrechen()
	switch strings.ToLower(u.Scheme) {
	case "https":
		return httpsHolen(ctx, u.String(), benutzer, passwort)
	case "sftp", "ftps", "ftp":
		return curlHolen(ctx, u, benutzer, passwort, fingerabdruck)
	}
	return nil, time.Time{}, Fehler("Adresse muss mit sftp://, ftps://, ftp:// oder https:// beginnen")
}

func httpsHolen(ctx context.Context, adresse, benutzer, passwort string) ([]byte, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adresse, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	if benutzer != "" {
		req.SetBasicAuth(benutzer, passwort)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, time.Time{}, Fehler("Übertragung fehlgeschlagen: " + err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, time.Time{}, Fehler(fmt.Sprintf("Übertragung fehlgeschlagen: HTTP %d", res.StatusCode))
	}
	daten, err := io.ReadAll(io.LimitReader(res.Body, MaxDatei+1))
	if err != nil {
		return nil, time.Time{}, Fehler("Übertragung abgebrochen: " + err.Error())
	}
	if len(daten) > MaxDatei {
		return nil, time.Time{}, Fehler("Die Datei ist grösser als 300 MB")
	}
	stand, _ := http.ParseTime(res.Header.Get("Last-Modified"))
	return daten, stand, nil
}

func curlHolen(ctx context.Context, u *url.URL, benutzer, passwort, fingerabdruck string) ([]byte, time.Time, error) {
	ablage, err := os.MkdirTemp("", "arthub-import-")
	if err != nil {
		return nil, time.Time{}, err
	}
	defer os.RemoveAll(ablage)
	ziel := filepath.Join(ablage, "liste")

	var konfig bytes.Buffer
	fmt.Fprintf(&konfig, "url = %q\n", u.String())
	fmt.Fprintf(&konfig, "silent\nshow-error\nfail\nmax-time = 1800\nconnect-timeout = 30\n")
	fmt.Fprintf(&konfig, "remote-time\noutput = %q\n", ziel)
	if benutzer != "" {
		fmt.Fprintf(&konfig, "user = %q\n", benutzer+":"+passwort)
	}
	var umgebung []string
	switch strings.ToLower(u.Scheme) {
	case "sftp":
		if fingerabdruck == "" {
			return nil, time.Time{}, Fehler("Fingerabdruck des Servers fehlt – zuerst «Verbindung prüfen» und bestätigen")
		}
		home, err := knownHostsAnlegen(ctx, u.String(), fingerabdruck)
		if err != nil {
			return nil, time.Time{}, err
		}
		defer os.RemoveAll(home)
		umgebung = append(os.Environ(), "HOME="+home, "CURL_HOME="+home)
		fmt.Fprintf(&konfig, "hostpubsha256 = %q\n", fingerabdruck)
		fmt.Fprintf(&konfig, "compressed-ssh\n") // CSV schrumpft stark; lehnt der Server ab, geht es unkomprimiert
	case "ftp":
		fmt.Fprintf(&konfig, "ssl\n") // Verschlüsselung verlangen, wenn der Server sie kann
	}

	befehl := exec.CommandContext(ctx, curlBefehl, "--config", "-")
	befehl.Env = umgebung
	befehl.Stdin = &konfig
	var fehler bytes.Buffer
	befehl.Stderr = &fehler
	if err := befehl.Run(); err != nil {
		text := strings.TrimSpace(fehler.String())
		if text == "" {
			text = err.Error()
		}
		return nil, time.Time{}, Fehler("Übertragung fehlgeschlagen: " + text)
	}
	info, err := os.Stat(ziel)
	if err != nil || info.Size() == 0 {
		return nil, time.Time{}, Fehler("Die Datei ist leer")
	}
	if info.Size() > MaxDatei {
		return nil, time.Time{}, Fehler("Die Datei ist grösser als 300 MB")
	}
	daten, err := os.ReadFile(ziel)
	if err != nil {
		return nil, time.Time{}, err
	}
	stand := info.ModTime()
	if stand.Year() < 2000 || stand.After(time.Now().Add(24*time.Hour)) {
		stand = time.Time{} // curl kennt das Datum nicht
	}
	return daten, stand, nil
}

// hostSchluessel: ein öffentlicher Schlüssel des Servers, wie ssh-keyscan ihn liefert
type hostSchluessel struct {
	zeile         string // Format von known_hosts
	art           string // ED25519, ECDSA, RSA
	fingerabdruck string // SHA256, Base64 ohne «=»
}

func hostSchluesselHolen(ctx context.Context, adresse string) ([]hostSchluessel, error) {
	u, err := url.Parse(strings.TrimSpace(adresse))
	if err != nil || u.Host == "" {
		return nil, Fehler("Adresse nicht verwendbar")
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "22"
	}
	ctx, abbrechen := context.WithTimeout(ctx, 30*time.Second)
	defer abbrechen()
	scan := exec.CommandContext(ctx, keyscanBefehl, "-p", port, "-t", "ed25519,ecdsa,rsa", host)
	var aus, scanFehler bytes.Buffer
	scan.Stdout, scan.Stderr = &aus, &scanFehler
	if err := scan.Run(); err != nil || aus.Len() == 0 {
		text := strings.TrimSpace(scanFehler.String())
		if text == "" {
			text = "keine Antwort"
		}
		return nil, Fehler("Server nicht erreichbar: " + text)
	}
	var liste []hostSchluessel
	for _, zeile := range strings.Split(aus.String(), "\n") {
		zeile = strings.TrimSpace(zeile)
		teile := strings.Fields(zeile)
		if len(teile) < 3 || strings.HasPrefix(zeile, "#") {
			continue
		}
		roh, err := base64.StdEncoding.DecodeString(teile[2])
		if err != nil {
			continue
		}
		summe := sha256.Sum256(roh)
		art := strings.ToUpper(strings.TrimPrefix(teile[1], "ssh-"))
		if strings.HasPrefix(teile[1], "ecdsa") {
			art = "ECDSA"
		}
		liste = append(liste, hostSchluessel{zeile: strings.Join(teile[:3], " "), art: art,
			fingerabdruck: base64.RawStdEncoding.EncodeToString(summe[:])})
	}
	if len(liste) == 0 {
		return nil, Fehler("Server liefert keinen lesbaren Hostschlüssel")
	}
	rang := map[string]int{"ED25519": 0, "ECDSA": 1, "RSA": 2}
	sort.SliceStable(liste, func(i, j int) bool {
		ri, ok := rang[liste[i].art]
		if !ok {
			ri = 3
		}
		rj, ok := rang[liste[j].art]
		if !ok {
			rj = 3
		}
		return ri < rj
	})
	return liste, nil
}

// Fingerabdruck fragt den Hostschlüssel eines SFTP-Servers ab (zum Bestätigen).
func Fingerabdruck(ctx context.Context, adresse string) (fingerabdruck, art string, err error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(adresse)), "sftp://") {
		return "", "", Fehler("Ein Fingerabdruck gibt es nur bei sftp://-Adressen")
	}
	liste, err := hostSchluesselHolen(ctx, adresse)
	if err != nil {
		return "", "", err
	}
	return liste[0].fingerabdruck, liste[0].art, nil
}

func knownHostsAnlegen(ctx context.Context, adresse, fingerabdruck string) (string, error) {
	liste, err := hostSchluesselHolen(ctx, adresse)
	if err != nil {
		return "", err
	}
	fingerabdruck = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(fingerabdruck), "SHA256:"), "=")
	var zeile string
	for _, h := range liste {
		if h.fingerabdruck == fingerabdruck {
			zeile = h.zeile
			break
		}
	}
	if zeile == "" {
		gefunden := make([]string, len(liste))
		for i, h := range liste {
			gefunden[i] = h.art + " SHA256:" + h.fingerabdruck
		}
		return "", Fehler("Der Server zeigt einen anderen Hostschlüssel als den bestätigten (SHA256:" + fingerabdruck +
			"; jetzt: " + strings.Join(gefunden, ", ") + "). Hat der Lieferant den Server gewechselt, «Verbindung prüfen» " +
			"und den neuen Fingerabdruck bestätigen – sonst nicht verbinden.")
	}
	home, err := os.MkdirTemp("", "arthub-ssh-")
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		os.RemoveAll(home)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(zeile+"\n"), 0o600); err != nil {
		os.RemoveAll(home)
		return "", err
	}
	return home, nil
}
