-- Recovery-authored display only. Consent, legal details, access, and
-- deployment stay separate. Existing intents are not given a plan.
CREATE TABLE gomar.native_signup_plans (
    intent TEXT COLLATE "C" NOT NULL,
    revision BIGINT NOT NULL,
    approval_ref TEXT COLLATE "C" NOT NULL,
    authored_by BIGINT NOT NULL REFERENCES gomar.users(id),
    authored_at TIMESTAMPTZ NOT NULL,
    organization TEXT NOT NULL CHECK(char_length(organization) BETWEEN 1 AND 160),
    branch_display JSONB NOT NULL CHECK(jsonb_typeof(branch_display)='array' AND jsonb_array_length(branch_display) BETWEEN 1 AND 64),
    PRIMARY KEY(intent,revision),
    FOREIGN KEY(intent,revision,approval_ref) REFERENCES gomar.native_owner_approvals(intent,revision,approval_ref)
);

CREATE INDEX native_owner_designations_claimant ON gomar.native_owner_designations(claimant_id,intent,revision);

CREATE TRIGGER native_signup_plans_immutable BEFORE UPDATE OR DELETE ON gomar.native_signup_plans
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
