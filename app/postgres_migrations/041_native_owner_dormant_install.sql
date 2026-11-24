-- Pending records must not appear in operational entities, outlets,
-- memberships, or outlet_grants. There is intentionally no active column.
ALTER TABLE gomar.native_owner_receipts ADD UNIQUE (intent,receipt_ref);

CREATE TABLE gomar.native_owner_install_drafts (
    intent TEXT COLLATE "C" PRIMARY KEY,
    consent_ref TEXT COLLATE "C" NOT NULL,
    approved_by BIGINT NOT NULL REFERENCES gomar.users(id),
    approved_at TIMESTAMPTZ NOT NULL,
    legal_name TEXT NOT NULL CHECK(char_length(legal_name) BETWEEN 1 AND 160),
    cuit TEXT COLLATE "C" NOT NULL CHECK(cuit ~ '^[0-9]{11}$'),
    initiator_ref TEXT COLLATE "C" NOT NULL CHECK(initiator_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
    claimant_ref TEXT COLLATE "C" NOT NULL CHECK(claimant_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
    branch_plan JSONB NOT NULL CHECK(jsonb_typeof(branch_plan)='array' AND jsonb_array_length(branch_plan) BETWEEN 1 AND 64),
    FOREIGN KEY(intent,consent_ref) REFERENCES gomar.native_owner_receipts(intent,receipt_ref),
    UNIQUE(intent,consent_ref,cuit),
    CHECK(initiator_ref<>claimant_ref)
);

-- The immutable receipt is itself the pending organization and one intended
-- administration assignment. Branch rows are only its exact proposed grants.
CREATE TABLE gomar.native_owner_pending_installs (
    intent TEXT COLLATE "C" PRIMARY KEY,
    consent_ref TEXT COLLATE "C" NOT NULL,
    install_ref TEXT COLLATE "C" NOT NULL UNIQUE CHECK(install_ref ~ '^pending-[A-Za-z0-9_-]{43}$'),
    cuit TEXT COLLATE "C" NOT NULL UNIQUE,
    planned_owner_id BIGINT NOT NULL REFERENCES gomar.users(id),
    planned_role TEXT COLLATE "C" NOT NULL CHECK(planned_role='administration'),
    billing_issuer TEXT COLLATE "C" NOT NULL CHECK(billing_issuer ~ '^[A-Za-z0-9_-]{1,128}$'),
    gomar_recipient TEXT COLLATE "C" NOT NULL CHECK(gomar_recipient ~ '^[A-Za-z0-9_-]{1,128}$'),
    nonce_hash BYTEA NOT NULL UNIQUE CHECK(octet_length(nonce_hash)=32),
    decision_checked_at TIMESTAMPTZ NOT NULL,
    decision_expires_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    CHECK(decision_checked_at<=recorded_at AND recorded_at<decision_expires_at
        AND decision_expires_at<=decision_checked_at+INTERVAL '5 seconds'),
    FOREIGN KEY(intent,consent_ref,cuit) REFERENCES gomar.native_owner_install_drafts(intent,consent_ref,cuit)
);

CREATE TABLE gomar.native_owner_pending_branches (
    install_ref TEXT COLLATE "C" NOT NULL REFERENCES gomar.native_owner_pending_installs(install_ref),
    branch_ref TEXT COLLATE "C" NOT NULL CHECK(branch_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
    branch_name TEXT COLLATE "C" NOT NULL CHECK(char_length(branch_name) BETWEEN 1 AND 160),
    PRIMARY KEY(install_ref,branch_ref),
    UNIQUE(install_ref,branch_name)
);

CREATE TRIGGER native_owner_install_drafts_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_install_drafts
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER native_owner_pending_installs_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_pending_installs
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER native_owner_pending_branches_immutable BEFORE UPDATE OR DELETE ON gomar.native_owner_pending_branches
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
