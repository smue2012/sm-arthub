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
  zeigen($("abmelden"), true);
  zeigen($("ansichten"), true);
  await ansichtZeigen();
  await Promise.all([statusLaden(), listeLaden(), lieferantenLaden(), laeufeLaden()]);
}

// ---------- Ansichten: Verwaltung | Katalog & Auswahl ----------
async function ansichtZeigen() {
  const katalog = location.hash === "#katalog";
  zeigen($("inhalt"), !katalog);
  zeigen($("katalog-ansicht"), katalog);
  document.querySelector("main").classList.toggle("breit", katalog);
  document.querySelectorAll("#ansichten a").forEach((a) =>
    a.classList.toggle("aktiv", (a.dataset.ansicht === "katalog") === katalog));
  if (katalog && !katalogGeladen) {
    katalogGeladen = true;
    try { await katalogLaden(); } catch (e) { if (e.status === 401) anmeldungZeigen(); else $("kat-zusammenfassung").textContent = e.message; }
  }
}

// ---------- Katalog & Auswahl je App ----------
let katalogGeladen = false;
const kat = { baum: [], offen: new Set(), pfad: "", q: "", lager: false, seite: 0, gesamt: 0, app: "", auswahl: null, artikel: [] };
let katTimer = null;

const artikelPfad = (a) => [a.kategorie, a.kategorie2, a.kategorie3].filter((x) => x).join(" > ");
// Ist «pfad» (oder ein übergeordneter Zweig) gewählt? Liefert den gewählten Zweig oder "".
function zweigGewaehlt(pfad) {
  if (!kat.auswahl || !pfad) return "";
  for (const k of kat.auswahl.kategorien) {
    if ((pfad + " >").startsWith(k + " >")) return k;
  }
  return "";
}

async function katalogLaden() {
  const [schl, baum] = await Promise.all([api("/api/schluessel"), api("/api/katalog/kategorien")]);
  const apps = schl.schluessel.map((x) => x.name);
  $("kat-app").replaceChildren(...apps.map((n) => new Option(n, n)));
  if (!kat.app || !apps.includes(kat.app)) kat.app = apps.includes("sst") ? "sst" : (apps[0] || "");
  $("kat-app").value = kat.app;
  kat.baum = baum.kategorien;
  $("kat-alle-anzahl").textContent = zahl(kat.baum.reduce((s, k) => s + k.anzahl, 0));
  await auswahlLaden();
  await artikelLaden();
}

async function auswahlLaden() {
  kat.auswahl = kat.app ? await api(`/api/auswahl/${encodeURIComponent(kat.app)}`) : null;
  auswahlAnzeigen();
}

function auswahlAnzeigen() {
  const a = kat.auswahl;
  $("kat-nur").disabled = !a;
  $("kat-nur").checked = !!(a && a.nur_auswahl);
  if (!a) {
    $("kat-zusammenfassung").textContent = "Noch keine Applikation – zuerst unter «Verwaltung» einen API-Schlüssel anlegen.";
  } else {
    const teile = `${a.kategorien.length} Kategorien, ${a.artikel.length} Einzelartikel`;
    $("kat-zusammenfassung").textContent = a.nur_auswahl
      ? `«${a.app}» erhält nur die Auswahl (${teile}): ${zahl(a.anzahl)} Artikel.`
      : `«${a.app}» erhält den ganzen Katalog. Auswahl (${teile}, ${zahl(a.anzahl)} Artikel) wirkt erst mit «Nur die Auswahl liefern».`;
  }
  baumZeichnen();
  artikelZeichnen();
}

async function eintragSetzen(art, wert, an) {
  try {
    kat.auswahl = await api(`/api/auswahl/${encodeURIComponent(kat.app)}/eintrag`, {
      method: "POST", body: JSON.stringify({ art, wert, an }),
    });
  } catch (e) {
    if (e.status === 401) { anmeldungZeigen(); return; }
    alert(e.message);
  }
  auswahlAnzeigen();
}

function baumZeichnen() {
  const knoten = (k) => {
    const li = document.createElement("li");
    const zeile = document.createElement("div");
    zeile.className = "knoten";
    const auf = document.createElement("button");
    auf.type = "button";
    auf.className = "auf";
    auf.disabled = !k.unter || !k.unter.length;
    auf.textContent = kat.offen.has(k.pfad) ? "▾" : "▸";
    auf.setAttribute("aria-label", kat.offen.has(k.pfad) ? "zuklappen" : "aufklappen");
    auf.addEventListener("click", () => {
      if (kat.offen.has(k.pfad)) kat.offen.delete(k.pfad); else kat.offen.add(k.pfad);
      baumZeichnen();
    });
    zeile.append(auf);
    if (kat.auswahl && k.pfad !== "(ohne Kategorie)") {
      const cb = document.createElement("input");
      cb.type = "checkbox";
      const direkt = kat.auswahl.kategorien.includes(k.pfad);
      const ueber = zweigGewaehlt(k.pfad);
      cb.checked = direkt || !!ueber;
      cb.disabled = !direkt && !!ueber;
      if (cb.disabled) { cb.title = `über «${ueber}» gewählt`; zeile.classList.add("geerbt"); }
      cb.setAttribute("aria-label", `${k.name} für ${kat.app} wählen`);
      cb.addEventListener("change", () => eintragSetzen("kategorie", k.pfad, cb.checked));
      zeile.append(cb);
    }
    const name = document.createElement("button");
    name.type = "button";
    name.className = "baum-name" + (kat.pfad === k.pfad ? " aktiv" : "");
    name.textContent = k.name;
    const n = document.createElement("span");
    n.textContent = zahl(k.anzahl);
    name.append(n);
    name.addEventListener("click", () => {
      kat.pfad = k.pfad; kat.seite = 0;
      if (k.unter && k.unter.length) kat.offen.add(k.pfad);
      baumZeichnen(); artikelLaden();
    });
    zeile.append(name);
    li.append(zeile);
    if (kat.offen.has(k.pfad) && k.unter) {
      const ul = document.createElement("ul");
      k.unter.forEach((u) => ul.append(knoten(u)));
      li.append(ul);
    }
    return li;
  };
  $("kat-baum").replaceChildren(...kat.baum.map(knoten));
  $("kat-alle").classList.toggle("aktiv", kat.pfad === "");
}

async function artikelLaden() {
  const p = new URLSearchParams({ seite: String(kat.seite) });
  if (kat.pfad) p.set("pfad", kat.pfad);
  if (kat.q) p.set("q", kat.q);
  if (kat.lager) p.set("verfuegbar", "1");
  $("kat-pfad").textContent = "Lade …";
  const d = await api(`/api/katalog/artikel?${p}`);
  kat.artikel = d.artikel; kat.gesamt = d.gesamt;
  const von = d.gesamt ? d.seite * d.pro_seite + 1 : 0, bis = d.seite * d.pro_seite + d.artikel.length;
  $("kat-pfad").textContent = `${kat.pfad || "Alle Artikel"}${kat.q ? ` · Suche «${kat.q}»` : ""}: ${zahl(d.gesamt)} Artikel`;
  $("kat-seite").textContent = d.gesamt ? `${zahl(von)}–${zahl(bis)} von ${zahl(d.gesamt)}` : "";
  $("kat-zurueck").disabled = d.seite === 0;
  $("kat-weiter").disabled = bis >= d.gesamt;
  artikelZeichnen();
}

function artikelZeichnen() {
  const tb = $("kat-artikel");
  tb.replaceChildren();
  const einzeln = new Set((kat.auswahl ? kat.auswahl.artikel : []).map((x) => x.nummer));
  for (const a of kat.artikel) {
    const tr = document.createElement("tr");
    const td = () => { const x = document.createElement("td"); tr.append(x); return x; };
    const wahl = td();
    wahl.className = "kat-wahl";
    if (kat.auswahl) {
      const cb = document.createElement("input");
      cb.type = "checkbox";
      const direkt = einzeln.has(a.nummer), ueber = zweigGewaehlt(artikelPfad(a));
      cb.checked = direkt || !!ueber;
      cb.disabled = !direkt && !!ueber;
      if (cb.disabled) cb.title = `über «${ueber}» gewählt`;
      cb.setAttribute("aria-label", `${a.bezeichnung} für ${kat.app} wählen`);
      cb.addEventListener("change", () => eintragSetzen("artikel", a.nummer, cb.checked));
      wahl.append(cb);
      tr.classList.toggle("gewaehlt", cb.checked);
    }
    const art = td();
    const b = document.createElement("strong");
    b.textContent = a.bezeichnung;
    const u = document.createElement("div");
    u.className = "unter";
    u.textContent = [a.nummer, a.hersteller, a.hersteller_nr, artikelPfad(a)].filter((x) => x).join(" · ");
    art.append(b, u);
    const best = a.bestes || {};
    const lief = td();
    lief.textContent = best.lieferant_name || "–";
    if (best.lieferanten_nr) { const x = document.createElement("div"); x.className = "unter"; x.textContent = best.lieferanten_nr; lief.append(x); }
    const lager = td();
    lager.className = "r" + (best.verfuegbar ? "" : " leer-bestand");
    lager.textContent = best.lieferant ? zahl(best.lagerbestand) : "–";
    const ek = td();
    ek.className = "r";
    ek.textContent = best.ek ? Number(best.ek).toLocaleString("de-CH", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) : "–";
    tb.append(tr);
  }
  if (!kat.artikel.length) {
    const tr = document.createElement("tr");
    const x = document.createElement("td");
    x.colSpan = 5; x.className = "hinweis"; x.textContent = "Keine Artikel.";
    tr.append(x); tb.append(tr);
  }
}

function anmeldungZeigen() {
  zeigen($("inhalt"), false);
  zeigen($("katalog-ansicht"), false);
  zeigen($("ansichten"), false);
  katalogGeladen = false;
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
    katalogGeladen = false; // neue App in der Auswahl-Liste
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
  if (l.art === "bestand" || l.art === "bestand_zeitplan") {
    const t = [`${zahl(b.zeilen)} Zeilen`, `${zahl(b.bestand_geaendert)} Bestände geändert`];
    if (b.unbekannt) t.push(`${zahl(b.unbekannt)} ohne Angebot`);
    t.push(`${b.sekunden ?? "–"} s`);
    return t.join(" · ");
  }
  const teile = [`${zahl(b.zeilen)} Zeilen`];
  if (b.artikel_neu) teile.push(`${zahl(b.artikel_neu)} Artikel neu`);
  if (b.zugeordnet) teile.push(`${zahl(b.zugeordnet)} bestehenden zugeordnet`);
  if (b.preise_geaendert != null) {
    if (b.preise_geaendert) teile.push(`${zahl(b.preise_geaendert)} EK geändert`);
    if (b.bestand_geaendert) teile.push(`${zahl(b.bestand_geaendert)} Bestände geändert`);
    if (b.reaktiviert) teile.push(`${zahl(b.reaktiviert)} wieder gelistet`);
  } else if (b.angebote_geaendert) teile.push(`${zahl(b.angebote_geaendert)} Preise/Bestände geändert`);
  if (b.stammdaten_geaendert) teile.push(`${zahl(b.stammdaten_geaendert)} Stammdaten geändert`);
  if (b.deaktiviert) teile.push(`${zahl(b.deaktiviert)} nicht mehr gelistet`);
  if (b.bilder_geaendert) teile.push(`${zahl(b.bilder_geaendert)} Bilder`);
  if (b.uebersprungen) teile.push(`${zahl(b.uebersprungen)} Zeilen übersprungen`);
  teile.push(`${b.sekunden ?? "–"} s`);
  return teile.join(" · ") + (b.hinweise && b.hinweise.length ? " – " + b.hinweise.join(" ") : "");
}
const STATUS = { laeuft: "läuft", ok: "ok", fehler: "Fehler" };
const ART = { abruf: "von Hand", datei: "Datei", zeitplan: "Zeitplan", bestand: "Bestand", bestand_zeitplan: "Bestand (Zeitplan)" };

async function laeufeLaden() {
  const { laeufe } = await api("/api/laeufe");
  const tb = $("laeufe-liste");
  tb.replaceChildren();
  for (const l of laeufe) {
    const tr = document.createElement("tr");
    const zellen = [datum(l.start), l.lieferant, ART[l.art] || l.art, STATUS[l.status] || l.status, laufText(l)];
    if (l.status === "ok" && !l.art.startsWith("bestand")) {
      tr.tabIndex = 0;
      tr.title = "Protokoll anzeigen";
      tr.addEventListener("click", () => protokollOeffnen(l));
      tr.addEventListener("keydown", (e) => { if (e.key === "Enter") protokollOeffnen(l); });
    }
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

// ---------- Protokoll eines Laufs ----------
let protokollLauf = null, protokollArt = "";

async function protokollOeffnen(l) {
  protokollLauf = l;
  protokollArt = "";
  $("pr-titel").textContent = `Importprotokoll ${l.lieferant} · ${datum(l.start)}`;
  $("pr-zusammenfassung").textContent = laufText(l);
  $("protokoll").showModal();
  await protokollLaden();
}

async function protokollLaden() {
  const art = protokollArt ? `?art=${encodeURIComponent(protokollArt)}` : "";
  $("pr-zeilen").replaceChildren();
  meldung($("pr-leer"), "Lade …");
  let d;
  try {
    d = await api(`/api/laeufe/${protokollLauf.id}/details${art}`);
  } catch (e) {
    if (e.status === 401) { $("protokoll").close(); anmeldungZeigen(); return; }
    meldung($("pr-leer"), e.message);
    return;
  }
  const gesamt = d.arten.reduce((s, a) => s + a.anzahl, 0);
  const knoepfe = [{ art: "", text: "Alle", anzahl: gesamt }, ...d.arten].map((a) => {
    const k = document.createElement("button");
    k.type = "button";
    k.setAttribute("role", "tab");
    k.setAttribute("aria-selected", String(a.art === protokollArt));
    k.textContent = `${a.text} (${zahl(a.anzahl)})`;
    k.disabled = !a.anzahl && a.art !== "";
    k.addEventListener("click", () => { protokollArt = a.art; protokollLaden(); });
    return k;
  });
  $("pr-arten").replaceChildren(...knoepfe);

  const texte = Object.fromEntries(d.arten.map((a) => [a.art, a.text]));
  for (const x of d.details) {
    const tr = document.createElement("tr");
    // Beträge «22.5000» als «22.50» (mehr Stellen nur, wenn sie zählen)
    const betrag = (v) => (/^\d+\.\d{4}$/.test(v) ? v.replace(/(\.\d\d\d*?)0+$/, "$1") : v);
    const zellen = [x.lieferanten_nr, x.artikel_nummer, x.bezeichnung, betrag(x.alt), betrag(x.neu)];
    zellen.forEach((t, i) => {
      const td = document.createElement("td");
      td.textContent = t;
      if (i < 2) td.className = "mono";
      if (i >= 3) td.className = "r";
      if (i === 2 && !protokollArt) td.title = texte[x.art] || x.art;
      tr.append(td);
    });
    if (!protokollArt) {
      const art = document.createElement("div");
      art.className = "ergebnis-text";
      art.textContent = texte[x.art] || x.art;
      tr.children[2].append(art);
    }
    $("pr-zeilen").append(tr);
  }
  const anzahl = protokollArt ? (d.arten.find((a) => a.art === protokollArt) || {}).anzahl : gesamt;
  meldung($("pr-leer"), d.details.length ? "" :
    "Keine Einträge. Reine Bestandsänderungen werden nur gezählt; Protokolle älter als 90 Tage sind gelöscht.");
  $("pr-hinweis").textContent = anzahl > d.details.length ? `Angezeigt: ${zahl(d.details.length)} von ${zahl(anzahl)} – alle in der CSV-Datei.` : "";
}

async function protokollCSV() {
  const art = protokollArt ? `?art=${encodeURIComponent(protokollArt)}` : "";
  const knopf = $("pr-csv");
  knopf.disabled = true;
  try {
    const antwort = await fetch(`/api/laeufe/${protokollLauf.id}/details.csv${art}`, { headers: { "X-Arthub": "1" }, credentials: "same-origin" });
    if (!antwort.ok) throw new Error(`Fehler ${antwort.status}`);
    const name = ((antwort.headers.get("Content-Disposition") || "").match(/filename="([^"]+)"/) || [])[1] || "importprotokoll.csv";
    const url = URL.createObjectURL(await antwort.blob());
    const a = document.createElement("a");
    a.href = url; a.download = name; document.body.append(a); a.click();
    setTimeout(() => { URL.revokeObjectURL(url); a.remove(); }, 1000);
  } catch (e) {
    $("pr-hinweis").textContent = e.message;
  } finally {
    knopf.disabled = false;
  }
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
  feld("bestand_url").value = q.bestand_url || "";
  feld("bestand_intervall").value = q.bestand_intervall || "";
  feld("bestand_von").value = q.bestand_von || "07:00";
  feld("bestand_bis").value = q.bestand_bis || "19:00";
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
        bestand_url: feld("bestand_url").value.trim(), bestand_intervall: Number(feld("bestand_intervall").value || 0),
        bestand_von: feld("bestand_von").value.trim(), bestand_bis: feld("bestand_bis").value.trim(),
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

  const starten = (art) => async (e) => {
    const knopf = e.currentTarget;
    meldung(fehlerEl, "");
    knopf.disabled = true;
    try {
      await speichern(); // ungespeicherte Änderungen zuerst übernehmen
      await api(`/api/lieferanten/${encodeURIComponent(q.kuerzel)}/importieren${art ? "?art=" + art : ""}`, { method: "POST" });
      await laeufeLaden();
    } catch (err) {
      if (err.status === 401) { anmeldungZeigen(); return; }
      meldung(fehlerEl, err.message);
    } finally {
      knopf.disabled = false;
    }
  };
  f.querySelector(".l-importieren").addEventListener("click", starten(""));
  f.querySelector(".l-bestand").addEventListener("click", starten("bestand"));

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
  window.addEventListener("hashchange", () => { if (!$("ansichten").hidden) ansichtZeigen(); });
  $("kat-app").addEventListener("change", async () => { kat.app = $("kat-app").value; await auswahlLaden(); });
  $("kat-nur").addEventListener("change", async () => {
    try {
      kat.auswahl = await api(`/api/auswahl/${encodeURIComponent(kat.app)}`, {
        method: "PUT", body: JSON.stringify({ nur_auswahl: $("kat-nur").checked }),
      });
    } catch (e) { alert(e.message); }
    auswahlAnzeigen();
  });
  $("kat-alle").addEventListener("click", () => { kat.pfad = ""; kat.seite = 0; baumZeichnen(); artikelLaden(); });
  $("kat-q").addEventListener("input", () => {
    clearTimeout(katTimer);
    katTimer = setTimeout(() => { kat.q = $("kat-q").value.trim(); kat.seite = 0; artikelLaden(); }, 350);
  });
  $("kat-lager").addEventListener("change", () => { kat.lager = $("kat-lager").checked; kat.seite = 0; artikelLaden(); });
  $("kat-zurueck").addEventListener("click", () => { kat.seite = Math.max(0, kat.seite - 1); artikelLaden(); });
  $("kat-weiter").addEventListener("click", () => { kat.seite++; artikelLaden(); });
  $("pr-zu").addEventListener("click", () => $("protokoll").close());
  $("pr-csv").addEventListener("click", protokollCSV);
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
