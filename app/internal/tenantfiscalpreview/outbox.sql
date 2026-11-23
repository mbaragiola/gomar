CREATE SCHEMA gomar_fiscal_fixture;

CREATE TABLE gomar_fiscal_fixture.channels (
    entity bigint NOT NULL CHECK (entity > 0),
    outlet bigint NOT NULL CHECK (outlet > 0),
    issuer text NOT NULL CHECK (issuer ~ '^fixture-[a-z][a-z0-9-]{0,39}$'),
    point integer NOT NULL CHECK (point BETWEEN 1 AND 99999),
    next_number integer NOT NULL DEFAULT 1 CHECK (next_number BETWEEN 1 AND 100000000),
    PRIMARY KEY (entity, outlet),
    UNIQUE (issuer, point),
    UNIQUE (entity, outlet, issuer, point)
);

CREATE TABLE gomar_fiscal_fixture.intents (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity bigint NOT NULL,
    outlet bigint NOT NULL,
    issuer text NOT NULL,
    point integer NOT NULL,
    sale bigint NOT NULL CHECK (sale > 0),
    actor bigint NOT NULL CHECK (actor > 0),
    total bigint NOT NULL CHECK (total BETWEEN 1 AND 10000000),
    issue_date date NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','claimed','unknown','authorized','refused','review')),
    number integer CHECK (number BETWEEN 1 AND 99999999),
    claim text CHECK (claim ~ '^[0-9a-f]{32}$'),
    expires_at timestamptz,
    code text CHECK (code ~ '^[0-9]{14}$' AND code <> '00000000000000'),
    code_expiry date,
    FOREIGN KEY (entity, outlet, issuer, point)
        REFERENCES gomar_fiscal_fixture.channels (entity, outlet, issuer, point),
    UNIQUE (entity, outlet, sale),
    UNIQUE (issuer, point, number),
    CHECK ((state = 'queued') = (number IS NULL)),
    CHECK ((state = 'claimed') = (claim IS NOT NULL AND expires_at IS NOT NULL)),
    CHECK (state = 'claimed' OR (claim IS NULL AND expires_at IS NULL)),
    CHECK ((state = 'authorized' AND code IS NOT NULL AND code_expiry >= issue_date AND code_expiry IS NOT NULL)
        OR (state <> 'authorized' AND code IS NULL AND code_expiry IS NULL))
);

CREATE UNIQUE INDEX fiscal_fixture_one_active_per_channel ON
    gomar_fiscal_fixture.intents (issuer, point)
    WHERE state IN ('claimed','unknown','refused','review');
