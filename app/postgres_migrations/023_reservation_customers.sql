ALTER TABLE reservation_venues ADD COLUMN collect_contact BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE outlets ADD CONSTRAINT reservation_contact_outlet_entity UNIQUE(id,entity_id);
ALTER TABLE reservation_requests ADD CONSTRAINT reservation_contact_request_outlet UNIQUE(id,outlet_id);

CREATE TABLE reservation_customers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id BIGINT NOT NULL REFERENCES entities(id),
    phone_e164 TEXT COLLATE "C" NOT NULL CHECK(phone_e164 ~ '^\+[1-9][0-9]{1,14}$'),
    UNIQUE(entity_id,phone_e164),
    UNIQUE(id,entity_id)
);

CREATE TABLE reservation_private_contacts (
    reservation_id TEXT COLLATE "C" PRIMARY KEY,
    outlet_id BIGINT NOT NULL,
    entity_id BIGINT NOT NULL,
    customer_id UUID NOT NULL,
    note TEXT NOT NULL CHECK(length(note) <= 300),
    FOREIGN KEY(reservation_id,outlet_id) REFERENCES reservation_requests(id,outlet_id) ON DELETE CASCADE,
    FOREIGN KEY(outlet_id,entity_id) REFERENCES outlets(id,entity_id),
    FOREIGN KEY(customer_id,entity_id) REFERENCES reservation_customers(id,entity_id)
);
