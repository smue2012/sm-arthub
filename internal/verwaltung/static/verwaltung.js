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
  await Promise.all([statusLaden(), listeLaden()]);
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

document.addEventListener("DOMContentLoaded", () => {
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
