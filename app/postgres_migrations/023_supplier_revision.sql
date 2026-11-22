-- Existing suppliers begin at revision 1; creation times and history are unchanged.
ALTER TABLE suppliers ADD COLUMN revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1);

CREATE FUNCTION gomar.gomar_advance_supplier_revision() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $revision$
BEGIN
    IF ROW(NEW.name, NEW.active, NEW.cuit) IS DISTINCT FROM ROW(OLD.name, OLD.active, OLD.cuit) THEN
        NEW.revision := OLD.revision + 1;
    ELSE
        NEW.revision := OLD.revision;
    END IF;
    RETURN NEW;
END;
$revision$;
CREATE TRIGGER supplier_revision_on_update BEFORE UPDATE ON suppliers
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_advance_supplier_revision();
