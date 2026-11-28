CREATE INDEX sessions_user_expiry ON sessions(user_id,expires);
CREATE INDEX admin_recovery_sessions_user_expiry ON admin_recovery_sessions(user_id,expires);
CREATE INDEX mcp_read_tokens_entity_expiry ON mcp_read_tokens(entity_id,expires_at,user_id);
CREATE INDEX mcp_read_tokens_expiry ON mcp_read_tokens(expires_at,id);

CREATE TABLE credential_prune_audit (
    utc_day DATE NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('sessions','admin_recovery_sessions','mcp_read_tokens')),
    removed BIGINT NOT NULL CHECK(removed>0),
    PRIMARY KEY(utc_day,kind)
);
