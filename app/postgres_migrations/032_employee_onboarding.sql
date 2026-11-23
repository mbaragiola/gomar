-- Employee credentials are not platform or provider credentials. The
-- synthetic core user provides stable ownership for native audit records.
CREATE TABLE organization_employees (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
    entity_id BIGINT NOT NULL REFERENCES entities(id),
    handle TEXT COLLATE "C" NOT NULL CHECK(handle ~ '^[a-z0-9][a-z0-9._-]{2,47}$'),
    status TEXT NOT NULL CHECK(status IN ('invited','changing_password','active','disabled')),
    password_hash TEXT,
    temporary_hash TEXT,
    temporary_expires_at BIGINT,
    change_previous_hash TEXT,
    change_hash BYTEA UNIQUE CHECK(octet_length(change_hash)=32),
    change_expires_at BIGINT,
    UNIQUE(entity_id,handle),
    FOREIGN KEY(user_id,entity_id) REFERENCES memberships(user_id,entity_id) ON DELETE RESTRICT,
    CHECK ((temporary_hash IS NULL) = (temporary_expires_at IS NULL)),
    CHECK ((change_hash IS NULL) = (change_expires_at IS NULL)),
    CHECK ((change_hash IS NULL) = (change_previous_hash IS NULL)),
    CHECK (
        (status='invited' AND temporary_hash IS NOT NULL AND change_hash IS NULL AND password_hash IS NULL)
        OR (status='changing_password' AND temporary_hash IS NULL AND change_hash IS NOT NULL AND password_hash IS NULL)
        OR (status='active' AND temporary_hash IS NULL AND change_hash IS NULL AND password_hash IS NOT NULL)
        OR (status='disabled' AND temporary_hash IS NULL AND change_hash IS NULL AND password_hash IS NULL)
    )
);

CREATE TABLE organization_employee_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    employee_id BIGINT NOT NULL REFERENCES organization_employees(user_id) ON DELETE RESTRICT,
    actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK(action IN ('invited','reset','disabled','activated')),
    occurred_at BIGINT NOT NULL
);
CREATE TRIGGER organization_employee_events_append_only BEFORE UPDATE OR DELETE ON organization_employee_events
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();

ALTER TABLE sessions DROP CONSTRAINT sessions_auth_method_check;
ALTER TABLE sessions ADD CONSTRAINT sessions_auth_method_check
    CHECK(auth_method IN ('password','provider','employee'));
