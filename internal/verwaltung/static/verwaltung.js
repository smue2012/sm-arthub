"use strict";

const $ = (id) => document.getElementById(id);

async function api(pfad, optionen = {}) {
  const antwort = await fetch(pfad, {
    ...optionen,
    headers: { "X-Arthub": "1", "Content-Type": "application/json", ...(optionen.headers || {}) },
    credentials: "same-origin",
  });
  let daten = {};
  try { daten = await antwort.json(); } catch { /* leer */ }
  if (!antwort.ok) {
    const err = new Error(daten.fehler || `Fehler ${antwort.status}`);
    err.status = antwort.status;
    throw err;
  }
  return daten;
}

function zeigen(el, sichtbar) { el.hidden = !sichtbar; }

function meldung(el, text) {
  el.textContent = text || "";
  zeigen(el, !!text);
}

const datum = (iso) => iso
  ? new Date(iso).toLocaleString("de-CH", { day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit" })
  : "nie";

async function laden() {
  try {
    await api("/api/ich");
  } catch (e) {
    if (e.status === 401) { anmeldungZeigen(); return; }
    throw e;
  }
  zeigen($("anmeldung"), false);
  zeigen($("inhalt"), true);
  zeigen($("abmelden"), true);
  await Promise.all([statusLaden(), listeLaden(), lieferantenLaden(), laeufeLaden()]);
}

function anmeldungZeigen() {
  zeigen($("inhalt"), false);
  zeigen($("abmelden"), false);
  zeigen($("anmeldung"), true);
  $("passwort").focus();
}

async function statusLaden() {
  const st = await api("/api/status");
  $("st-artikel").textContent = st.artikel.toLocaleString("de-CH");
  $("st-angebote").textContent = st.angebote.toLocaleString("de-CH");
  $("st-lieferanten").textContent = st.lieferanten.toLocaleString("de-CH");
  $("st-version").textContent = st.version;
  $("st-version").title = `Migration ${st.migration}`;
}

async function listeLaden() {
  const { schluessel } = await api("/api/schluessel");
  const tbody = $("schluessel-liste");
  tbody.replaceChildren();
  for (const s of schluessel) {
    const zeile = $("tpl-zeile").content.firstElementChild.cloneNode(true);
    zeile.querySelector(".name").textContent = s.name;
    const status = zeile.querySelector(".status");
    status.textContent = s.aktiv ? "aktiv" : "gesperrt";
    status.classList.add(s.aktiv ? "aktiv" : "gesperrt");
    zeile.querySelector(".erstellt").textContent = datum(s.erstellt);
    zeile.querySelector(".zuletzt").textContent = datum(s.zuletzt);
    const sperren = zeile.querySelector(".sperren");
    sperren.hidden = !s.aktiv;
    sperren.addEventListener("click", () => sperrenKlick(s.name));
    const ersetzen = zeile.querySelector(".ersetzen");
    ersetzen.textContent = s.aktiv ? "Neu erzeugen" : "Wieder freigeben";
    ersetzen.addEventListener("click", () => erzeugen(s.name, true));
    tbody.append(zeile);
  }
  zeigen($("leer-hinweis"), schluessel.length === 0);
}

async function erzeugen(name, bestehend) {
  meldung($("neu-fehler"), "");
  if (bestehend && !confirm(`Neuen Schlüssel für «${name}» erzeugen?\nDer bisherige Schlüssel gilt danach nicht mehr.`)) return;
  try {
    const erg = await api("/api/schluessel", { method: "POST", body: JSON.stringify({ name }) });
    $("erg-name").textContent = erg.name;
    $("erg-schluessel").textContent = erg.schluessel;
    $("kopieren").textContent = "Kopieren";
    zeigen($("ergebnis"), true);
    $("neu-name").value = "";
    await listeLaden();
  } catch (e) {
    if (e.status === 401) { anmeldungZeigen(); return; }
    meldung($("neu-fehler"), e.message);
  }
}

async function sperrenKlick(name) {
  if (!confirm(`Schlüssel von «${name}» sperren?\nDie Applikation kann den Katalog danach nicht mehr lesen.`)) return;
  try {
    await api(`/api/schluessel/${encodeURIComponent(name)}/sperren`, { method: "POST" });
    await listeLaden();
  } catch (e) {
    if (e.status === 401) { anmeldungZeigen(); return; }
    alert(e.message);
  }
}

// ---------- Lieferanten und Importe ----------
const zahl = (n) => Number(n || 0).toLocaleString("de-CH");
let formate = {};
let laeufeTimer = null;

function laufText(l) {
  if (l.status === "laeuft") return l.schritt ? `${l.schritt} …` : "läuft …";
  if (l.status === "fehler") return l.meldung;
  const b = l.bericht || {};
  const teile = [`${zahl(b.zeilen)} Zeilen`];
  if (b.artikel_neu) teile.push(`${zahl(b.artikel_neu)} Artikel neu`);
  if (b.zugeordnet) teile.push(`${zahl(b.zugeordnet)} bestehenden zugeordnet`);
  if (b.angebote_geaendert) teile.push(`${zahl(b.angebote_geaendert)} Preise/Bestände geändert`);
  if (b.stammdaten_geaendert) teile.push(`${zahl(b.stammdaten_geaendert)} Stammdaten geändert`);
  if (b.deaktiviert) teile.push(`${zahl(b.deaktiviert)} nicht mehr gelistet`);
  if (b.bilder_geaendert) teile.push(`${zahl(b.bilder_geaendert)} Bilder`);
  if (b.uebersprungen) teile.push(`${zahl(b.uebersprungen)} Zeilen übersprungen`);
  teile.push(`${b.sekunden ?? "–"} s`);
  return teile.join(" · ") + (b.hinweise && b.hinweise.length ? " – " + b.hinweise.join(" ") : "");
}
const STATUS = { laeuft: "läuft", ok: "ok", fehler: "Fehler" };
const ART = { abruf: "von Hand", datei: "Datei", zeitplan: "Zeitplan" };

async function laeufeLaden() {
  const { laeufe } = await api("/api/laeufe");
  const tb = $("laeufe-liste");
  tb.replaceChildren();
  for (const l of laeufe) {
    const tr = document.createElement("tr");
    const zellen = [datum(l.start), l.lieferant, ART[l.art] || l.art, STATUS[l.status] || l.status, laufText(l)];
    zellen.forEach((t, i) => {
      const td = document.createElement("td");
      td.textContent = t;
      if (i === 3) td.className = "status-" + l.status;
      if (i === 4) td.className = "ergebnis-text";
      tr.append(td);
    });
    tb.append(tr);
  }
  zeigen($("laeufe-leer"), laeufe.length === 0);
  clearTimeout(laeufeTimer);
  if (laeufe.some((l) => l.status === "laeuft")) {
    laeufeTimer = setTimeout(async () => {
      try { await laeufeLaden(); } catch { /* nächster Versuch beim Neuladen */ }
    }, 3000);
  } else if (laeufeLaden.lief) {
    statusLaden().catch(() => {});
    standAktualisieren().catch(() => {});
  }
  laeufeLaden.lief = laeufe.some((l) => l.status === "laeuft");
}

function standText(q) {
  const teile = [`${zahl(q.angebote)} aktive Angebote`];
  const l = q.letzter_lauf;
  if (l) teile.push(`letzter Import ${datum(l.start)}: ${STATUS[l.status] || l.status}`);
  return teile.join(" · ");
}

async function standAktualisieren() {
  const { lieferanten } = await api("/api/lieferanten");
  for (const q of lieferanten) {
    const f = document.querySelector(`form.lieferant[data-kuerzel="${CSS.escape(q.kuerzel)}"] .l-stand`);
    if (f) f.textContent = standText(q);
  }
}

async function lieferantenLaden() {
  const daten = await api("/api/lieferanten");
  formate = daten.formate || {};
  $("ln-format").replaceChildren(...Object.entries(formate).map(([k, v]) => new Option(v, k)));
  const ziel = $("lieferanten-liste");
  ziel.replaceChildren();
  for (const q of daten.lieferanten) ziel.append(lieferantFormular(q));
  zeigen($("lief-leer"), daten.lieferanten.length === 0);
}

function lieferantFormular(q) {
  const f = $("tpl-lieferant").content.firstElementChild.cloneNode(true);
  f.dataset.kuerzel = q.kuerzel;
  f.querySelector(".l-name").textContent = q.name;
  f.querySelector(".l-kuerzel").textContent = q.kuerzel + (q.format ? " · " + (formate[q.format] || q.format) : "");
  f.querySelector(".l-stand").textContent = standText(q);
  const feld = (n) => f.elements.namedItem(n);
  feld("url").value = q.url || "";
  feld("benutzer").value = q.benutzer || "";
  feld("passwort").placeholder = q.passwort_gesetzt ? "gespeichert (leer = unverändert)" : "";
  feld("fingerabdruck").value = q.fingerabdruck || "";
  feld("bildliste_url").value = q.bildliste_url || "";
  feld("zeit").value = q.zeit || "";
  feld("aktiv").checked = !!q.aktiv;
  const fehlerEl = f.querySelector(".l-fehler");

  if (!q.import) {
    // Lieferant ohne Preisliste (z.B. für eigene Artikel): nur Kopf zeigen
    f.querySelector(".feld-gitter").hidden = true;
    f.querySelector(".lief-aktionen").hidden = true;
    return f;
  }

  const speichern = async () => {
    await api(`/api/lieferanten/${encodeURIComponent(q.kuerzel)}/import`, {
      method: "PUT",
      body: JSON.stringify({
        format: q.format, url: feld("url").value.trim(), benutzer: feld("benutzer").value.trim(),
        passwort: feld("passwort").value, fingerabdruck: feld("fingerabdruck").value.trim(),
        bildliste_url: feld("bildliste_url").value.trim(), zeit: feld("zeit").value.trim(), aktiv: feld("aktiv").checked,
      }),
    });
    if (feld("passwort").value) {
      feld("passwort").value = "";
      feld("passwort").placeholder = "gespeichert (leer = unverändert)";
    }
  };

  f.addEventListener("submit", async (e) => {
    e.preventDefault();
    const knopf = f.querySelector("button[type=submit]");
    meldung(fehlerEl, "");
    knopf.disabled = true;
    try {
      await speichern();
      knopf.textContent = "Gespeichert";
      setTimeout(() => { knopf.textContent = "Speichern"; }, 2000);
    } catch (err) {
      if (err.status === 401) { anmeldungZeigen(); return; }
      meldung(fehlerEl, err.message);
    } finally {
      knopf.disabled = false;
    }
  });

  f.querySelector(".l-pruefen").addEventListener("click", async (e) => {
    const knopf = e.currentTarget;
    const info = f.querySelector(".l-fp-info");
    meldung(fehlerEl, "");
    knopf.disabled = true;
    knopf.textContent = "Prüfe …";
    try {
      const r = await api("/api/fingerabdruck", { method: "POST", body: JSON.stringify({ url: feld("url").value.trim() }) });
      const bisher = feld("fingerabdruck").value.trim();
      if (bisher && bisher.replace(/^SHA256:/, "") === r.fingerabdruck) {
        info.textContent = `Server erreichbar, Fingerabdruck stimmt (${r.art}).`;
      } else {
        feld("fingerabdruck").value = r.fingerabdruck;
        info.textContent = `${r.art}-Schlüssel des Servers übernommen${bisher ? " – ACHTUNG: anders als bisher" : ""}. ` +
          "Wenn möglich beim Lieferanten bestätigen lassen, dann speichern.";
      }
      info.hidden = false;
    } catch (err) {
      meldung(fehlerEl, err.message);
    } finally {
      knopf.disabled = false;
      knopf.textContent = "Verbindung prüfen";
    }
  });

  f.querySelector(".l-importieren").addEventListener("click", async (e) => {
    const knopf = e.currentTarget;
    meldung(fehlerEl, "");
    knopf.disabled = true;
    try {
      await speichern(); // ungespeicherte Änderungen zuerst übernehmen
      await api(`/api/lieferanten/${encodeURIComponent(q.kuerzel)}/importieren`, { method: "POST" });
      await laeufeLaden();
    } catch (err) {
      if (err.status === 401) { anmeldungZeigen(); return; }
      meldung(fehlerEl, err.message);
    } finally {
      knopf.disabled = false;
    }
  });

  f.querySelector(".l-datei").addEventListener("change", async (e) => {
    const eingabe = e.currentTarget;
    const datei = eingabe.files[0];
    eingabe.value = "";
    if (!datei) return;
    meldung(fehlerEl, "");
    const etikett = f.querySelector(".datei-knopf");
    const text = etikett.firstChild;
    text.textContent = `Lade ${datei.name} hoch …`;
    try {
      const fd = new FormData();
      fd.append("datei", datei);
      const antwort = await fetch(`/api/lieferanten/${encodeURIComponent(q.kuerzel)}/hochladen`, {
        method: "POST", body: fd, headers: { "X-Arthub": "1" }, credentials: "same-origin",
      });
      const r = await antwort.json().catch(() => ({}));
      if (!antwort.ok) throw Object.assign(new Error(r.fehler || `Fehler ${antwort.status}`), { status: antwort.status });
      await laeufeLaden();
    } catch (err) {
      if (err.status === 401) { anmeldungZeigen(); return; }
      meldung(fehlerEl, err.message);
    } finally {
      text.textContent = "Datei hochladen …";
    }
  });
  return f;
}

document.addEventListener("DOMContentLoaded", () => {
  $("lief-neu-knopf").addEventListener("click", () => {
    zeigen($("lief-neu"), true);
    $("ln-kuerzel").focus();
  });
  $("ln-abbrechen").addEventListener("click", () => zeigen($("lief-neu"), false));
  $("lief-neu").addEventListener("submit", async (e) => {
    e.preventDefault();
    meldung($("ln-fehler"), "");
    try {
      await api("/api/lieferanten", { method: "POST", body: JSON.stringify({
        kuerzel: $("ln-kuerzel").value.trim().toLowerCase(), name: $("ln-name").value.trim(), format: $("ln-format").value,
      }) });
      $("lief-neu").reset();
      zeigen($("lief-neu"), false);
      await lieferantenLaden();
    } catch (err) {
      meldung($("ln-fehler"), err.message);
    }
  });

  $("anmelde-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const knopf = e.currentTarget.querySelector("button");
    knopf.disabled = true;
    meldung($("anmelde-fehler"), "");
    try {
      await api("/api/anmelden", { method: "POST", body: JSON.stringify({ passwort: $("passwort").value }) });
      $("passwort").value = "";
      await laden();
    } catch (err) {
      meldung($("anmelde-fehler"), err.message);
    } finally {
      knopf.disabled = false;
    }
  });

  $("neu-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const name = $("neu-name").value.trim();
    const vorhanden = [...document.querySelectorAll("#schluessel-liste .name")].some((td) => td.textContent === name);
    erzeugen(name, vorhanden);
  });

  $("kopieren").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText($("erg-schluessel").textContent);
      $("kopieren").textContent = "Kopiert";
    } catch {
      const bereich = document.createRange();
      bereich.selectNodeContents($("erg-schluessel"));
      getSelection().removeAllRanges();
      getSelection().addRange(bereich);
      $("kopieren").textContent = "Markiert – mit Strg+C kopieren";
    }
  });

  $("ergebnis-schliessen").addEventListener("click", () => {
    $("erg-schluessel").textContent = "";
    zeigen($("ergebnis"), false);
  });

  $("abmelden").addEventListener("click", async () => {
    await api("/api/abmelden", { method: "POST" }).catch(() => {});
    $("erg-schluessel").textContent = "";
    zeigen($("ergebnis"), false);
    anmeldungZeigen();
  });

  laden().catch((e) => alert(e.message));
});
