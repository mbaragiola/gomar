CREATE TABLE reservation_public_businesses (
    slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 48),
    entity_id bigint NOT NULL UNIQUE REFERENCES entities(id),
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 120),
    UNIQUE (slug, entity_id)
);

CREATE TABLE reservation_public_branches (
    business_slug text NOT NULL,
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 48),
    entity_id bigint NOT NULL,
    outlet_id bigint NOT NULL UNIQUE,
    venue text NOT NULL CHECK (venue ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(venue) <= 48),
    revision bigint NOT NULL CHECK (revision > 0),
    enabled boolean NOT NULL DEFAULT false,
    PRIMARY KEY (business_slug, slug),
    FOREIGN KEY (business_slug, entity_id) REFERENCES reservation_public_businesses(slug, entity_id),
    FOREIGN KEY (outlet_id, entity_id) REFERENCES outlets(id, entity_id)
);

CREATE FUNCTION preserve_reservation_publication_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'reservation publication keys are retained' USING ERRCODE = '23514';
    END IF;
    IF TG_TABLE_NAME = 'reservation_public_businesses' THEN
        IF (NEW.slug, NEW.entity_id) IS DISTINCT FROM (OLD.slug, OLD.entity_id) THEN
            RAISE EXCEPTION 'reservation business scope is immutable' USING ERRCODE = '23514';
        END IF;
    ELSIF (NEW.business_slug, NEW.slug, NEW.entity_id, NEW.outlet_id)
          IS DISTINCT FROM (OLD.business_slug, OLD.slug, OLD.entity_id, OLD.outlet_id)
          OR NEW.revision <= OLD.revision THEN
        RAISE EXCEPTION 'reservation branch scope is immutable and revisions must increase' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER reservation_public_business_scope BEFORE UPDATE OR DELETE ON reservation_public_businesses
    FOR EACH ROW EXECUTE FUNCTION preserve_reservation_publication_scope();
CREATE TRIGGER reservation_public_branch_scope BEFORE UPDATE OR DELETE ON reservation_public_branches
    FOR EACH ROW EXECUTE FUNCTION preserve_reservation_publication_scope();

CREATE TABLE reservation_public_booking_keys (
    outlet_id bigint NOT NULL REFERENCES outlets(id),
    booking_key text NOT NULL CHECK (booking_key ~ '^[a-f0-9]{32}$'),
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
    reservation_id text NOT NULL UNIQUE REFERENCES reservation_requests(id),
    PRIMARY KEY (outlet_id, booking_key)
);
