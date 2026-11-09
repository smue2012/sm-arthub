-- Artikelnummer mit Prüfziffer (Entscheid Stephan, 09.11.2026): AH + 7 Ziffern
-- + Luhn-Prüfziffer, z.B. AH00000018. Eindeutig über den Zähler, die
-- Prüfziffer erkennt Tippfehler (eine falsche Ziffer, zwei vertauschte).
-- Die Nummer dient auch als SelectLine-Artikelnummer und ändert sich nie,
-- auch wenn das beste Angebot den Lieferanten wechselt.

CREATE FUNCTION luhn_pruefziffer(ziffern TEXT) RETURNS INTEGER AS $$
DECLARE
    summe INTEGER := 0;
    z INTEGER;
    i INTEGER;
BEGIN
    -- von rechts: jede zweite Ziffer verdoppeln, beginnend mit der letzten
    FOR i IN 0 .. length(ziffern) - 1 LOOP
        z := substr(ziffern, length(ziffern) - i, 1)::INTEGER;
        IF i % 2 = 0 THEN
            z := z * 2;
            IF z > 9 THEN z := z - 9; END IF;
        END IF;
        summe := summe + z;
    END LOOP;
    RETURN (10 - summe % 10) % 10;
END;
$$ LANGUAGE plpgsql IMMUTABLE STRICT;

CREATE FUNCTION artikel_nummer_neu() RETURNS TEXT AS $$
DECLARE
    n BIGINT := nextval('artikel_nummer_seq');
    ziffern TEXT;
BEGIN
    IF n > 9999999 THEN
        RAISE EXCEPTION 'Artikelnummern aufgebraucht (AH + 7 Ziffern)';
    END IF;
    ziffern := lpad(n::TEXT, 7, '0');
    RETURN 'AH' || ziffern || luhn_pruefziffer(ziffern);
END;
$$ LANGUAGE plpgsql;

ALTER TABLE artikel ALTER COLUMN nummer SET DEFAULT artikel_nummer_neu();

-- Bestehende Nummern ohne Prüfziffer ergänzen
UPDATE artikel SET nummer = nummer || luhn_pruefziffer(substr(nummer, 3))
WHERE nummer ~ '^AH[0-9]{7}$';
