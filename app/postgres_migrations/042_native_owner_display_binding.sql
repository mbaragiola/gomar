-- Existing receipts remain immutable, and old receipts/drafts are unbound.
-- Do not reconstruct approval from data the owner may never have seen.
ALTER TABLE gomar.native_owner_receipts
    ADD COLUMN display_digest BYTEA CHECK(octet_length(display_digest)=32);

-- This is the owner-reviewed display name, not the independently vetted
-- recovery legal name or CUIT. Existing drafts have no such evidence.
ALTER TABLE gomar.native_owner_install_drafts
    ADD COLUMN display_organization TEXT CHECK(char_length(display_organization) BETWEEN 1 AND 160);
