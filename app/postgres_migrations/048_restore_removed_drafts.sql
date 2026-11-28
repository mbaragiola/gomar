ALTER TABLE document_audit DROP CONSTRAINT document_audit_action_check,
    ADD CONSTRAINT document_audit_action_check CHECK (action IN ('create', 'update', 'delete', 'review', 'attach', 'restore'));

CREATE INDEX documents_removed_drafts_outlet_idx ON documents (entity_id, outlet_id, id DESC)
    WHERE status = 'draft' AND deleted_at IS NOT NULL;
