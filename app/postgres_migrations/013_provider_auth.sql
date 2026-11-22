CREATE TABLE provider_auth_flows (
    state_hash BYTEA PRIMARY KEY CHECK(octet_length(state_hash)=32),
    browser_hash BYTEA NOT NULL CHECK(octet_length(browser_hash)=32),
    nonce_hash BYTEA NOT NULL CHECK(octet_length(nonce_hash)=32),
    provider_flow TEXT NOT NULL CHECK(provider_flow IN ('google','apple','microsoft','microsoft-work')),
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL CHECK(expires_at>created_at AND expires_at<=created_at+300)
);
CREATE INDEX provider_auth_flows_expiry ON provider_auth_flows(expires_at);

CREATE TABLE provider_identities (
    provider TEXT NOT NULL CHECK(provider IN ('google','apple','microsoft')),
    issuer TEXT COLLATE "C" NOT NULL CHECK(length(issuer) BETWEEN 1 AND 255),
    subject TEXT COLLATE "C" NOT NULL CHECK(octet_length(subject) BETWEEN 1 AND 255),
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
    created_at BIGINT NOT NULL,
    PRIMARY KEY(provider,issuer,subject)
);

-- Existing app cookies remain valid in the default compatible mode.
-- The explicit provider-only mode only accepts sessions issued by OIDC.
ALTER TABLE sessions ADD COLUMN auth_method TEXT NOT NULL DEFAULT 'password'
    CHECK(auth_method IN ('password','provider'));
