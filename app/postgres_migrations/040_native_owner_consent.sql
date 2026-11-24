-- Internal evidence only. No users, entities, outlets, access grants or
-- entitlement are provisioned by these tables or by applying the migration.
CREATE FUNCTION gomar.native_owner_outlets_are_exact(outlet_refs TEXT[]) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE STRICT SET search_path = pg_catalog AS $native$
    SELECT cardinality(outlet_refs) BETWEEN 1 AND 64
       AND outlet_refs = ARRAY(
           SELECT DISTINCT item COLLATE "C" AS ref FROM unnest(outlet_refs) AS item
           WHERE item COLLATE "C" ~ '^[A-Za-z0-9_-]{1,128}$' ORDER BY ref
       );
$native$;

CREATE TABLE gomar.native_owner_intents (
    intent TEXT COLLATE "C" PRIMARY KEY CHECK(intent ~ '^[A-Za-z0-9_-]{1,128}$'),
    business TEXT COLLATE "C" NOT NULL CHECK(business ~ '^[A-Za-z0-9_-]{1,128}$'),
    audience TEXT COLLATE "C" NOT NULL CHECK(audience ~ '^[A-Za-z0-9_-]{1,128}$')
);

CREATE TABLE gomar.native_owner_designations (
    intent TEXT COLLATE "C" NOT NULL REFERENCES gomar.native_owner_intents(intent),
    revision BIGINT NOT NULL CHECK(revision>0),
    request_ref TEXT COLLATE "C" NOT NULL UNIQUE CHECK(request_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
    claimant_id BIGINT NOT NULL REFERENCES gomar.users(id),
    designated_by BIGINT NOT NULL REFERENCES gomar.users(id),
    outlets TEXT[] NOT NULL CHECK(gomar.native_owner_outlets_are_exact(outlets)),
    designated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY(intent,revision),
    CHECK(claimant_id<>designated_by),
    CHECK(designated_at<expires_at AND expires_at<=designated_at+INTERVAL '24 hours')
);

CREATE TABLE gomar.native_owner_approvals (
    intent TEXT COLLATE "C" NOT NULL,
    revision BIGINT NOT NULL,
    approval_ref TEXT COLLATE "C" NOT NULL UNIQUE CHECK(approval_ref ~ '^approval-[A-Za-z0-9_-]{43}$'),
    operator_id BIGINT NOT NULL REFERENCES gomar.users(id),
    approved_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY(intent,revision),
    UNIQUE(intent,revision,approval_ref),
    FOREIGN KEY(intent,revision) REFERENCES gomar.native_owner_designations(intent,revision),
    CHECK(approved_at<expires_at AND expires_at<=approved_at+INTERVAL '30 minutes')
);

CREATE TABLE gomar.native_owner_approval_revocations (
    approval_ref TEXT COLLATE "C" PRIMARY KEY REFERENCES gomar.native_owner_approvals(approval_ref),
    operator_id BIGINT NOT NULL REFERENCES gomar.users(id),
    revoked_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE gomar.native_owner_previews (
    challenge_hash BYTEA PRIMARY KEY CHECK(octet_length(challenge_hash)=32),
    session_hash BYTEA NOT NULL CHECK(octet_length(session_hash)=32),
    intent TEXT COLLATE "C" NOT NULL,
    revision BIGINT NOT NULL,
    approval_ref TEXT COLLATE "C" NOT NULL,
    previewed_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    UNIQUE(challenge_hash,intent,revision,approval_ref),
    FOREIGN KEY(intent,revision,approval_ref) REFERENCES gomar.native_owner_approvals(intent,revision,approval_ref),
    CHECK(previewed_at<expires_at AND expires_at<=previewed_at+INTERVAL '5 minutes')
);

-- One insert is both consumption and receipt, once per intent even across revisions.
CREATE TABLE gomar.native_owner_receipts (
    receipt_ref TEXT COLLATE "C" PRIMARY KEY CHECK(receipt_ref ~ '^accept-[A-Za-z0-9_-]{43}$'),
    challenge_hash BYTEA NOT NULL UNIQUE,
    intent TEXT COLLATE "C" NOT NULL,
    revision BIGINT NOT NULL,
    approval_ref TEXT COLLATE "C" NOT NULL,
    accepted_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    UNIQUE(intent),
    FOREIGN KEY(challenge_hash,intent,revision,approval_ref)
        REFERENCES gomar.native_owner_previews(challenge_hash,intent,revision,approval_ref),
    CHECK(accepted_at<expires_at AND expires_at<=accepted_at+INTERVAL '5 minutes')
);

CREATE TRIGGER native_owner_intents_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_intents
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER native_owner_designations_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_designations
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER native_owner_approvals_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_approvals
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER native_owner_revocations_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_approval_revocations
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER native_owner_previews_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_previews
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER native_owner_receipts_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_receipts
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
