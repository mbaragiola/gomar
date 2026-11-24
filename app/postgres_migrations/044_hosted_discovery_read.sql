-- An isolated discovery read overlay; no existing entity is classified.
CREATE TABLE gomar.discovery_entity_class (
    entity_id BIGINT PRIMARY KEY REFERENCES gomar.entities(id),
    class TEXT COLLATE "C" NOT NULL CHECK(class IN ('paid_hosted','self_host')),
    UNIQUE(entity_id,class)
);

-- Paid scope is not a CUIT or legal-entity assignment. Its exact branch
-- set and each individual mapping are immutable.
CREATE TABLE gomar.discovery_paid_scope (
    business_ref TEXT COLLATE "C" PRIMARY KEY CHECK(business_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
    branches TEXT[] COLLATE "C" NOT NULL CHECK(gomar.native_owner_outlets_are_exact(branches)),
    UNIQUE(business_ref,branches)
);

CREATE TABLE gomar.discovery_paid_outlet (
    outlet_id BIGINT PRIMARY KEY,
    entity_id BIGINT NOT NULL,
    class TEXT COLLATE "C" NOT NULL DEFAULT 'paid_hosted' CHECK(class='paid_hosted'),
    business_ref TEXT COLLATE "C" NOT NULL REFERENCES gomar.discovery_paid_scope(business_ref),
    branch_ref TEXT COLLATE "C" NOT NULL CHECK(branch_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
    UNIQUE(business_ref,branch_ref),
    FOREIGN KEY(outlet_id,entity_id) REFERENCES gomar.outlets(id,entity_id),
    FOREIGN KEY(entity_id,class) REFERENCES gomar.discovery_entity_class(entity_id,class)
);

-- No runtime producer is installed. Identical evidence cannot be
-- duplicated; different claims for the same highest epoch are retained
-- as disputed evidence and cannot authorize discovery.
CREATE TABLE gomar.discovery_scope_epoch (
    business_ref TEXT COLLATE "C" NOT NULL,
    branches TEXT[] COLLATE "C" NOT NULL,
    epoch BIGINT NOT NULL CHECK(epoch>0),
    state TEXT COLLATE "C" NOT NULL CHECK(state IN ('eligible','suspended','unknown')),
    verified_at TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NOT NULL,
    PRIMARY KEY(business_ref,epoch,state,verified_at,valid_until),
    FOREIGN KEY(business_ref,branches) REFERENCES gomar.discovery_paid_scope(business_ref,branches),
    CHECK(verified_at < valid_until AND valid_until <= verified_at + INTERVAL '5 seconds')
);

CREATE TRIGGER discovery_entity_class_immutable BEFORE UPDATE OR DELETE ON gomar.discovery_entity_class
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER discovery_paid_scope_immutable BEFORE UPDATE OR DELETE ON gomar.discovery_paid_scope
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER discovery_paid_outlet_immutable BEFORE UPDATE OR DELETE ON gomar.discovery_paid_outlet
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER discovery_scope_epoch_immutable BEFORE UPDATE OR DELETE ON gomar.discovery_scope_epoch
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
