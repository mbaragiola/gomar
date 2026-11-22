CREATE TABLE admin_recovery_sessions (
    token_hash BYTEA PRIMARY KEY CHECK(octet_length(token_hash)=32),
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_digest BYTEA NOT NULL CHECK(octet_length(password_digest)=32),
    expires BIGINT NOT NULL
);
CREATE INDEX admin_recovery_sessions_expires ON admin_recovery_sessions(expires);
