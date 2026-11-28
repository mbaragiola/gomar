CREATE INDEX intake_tracked_storage ON intake_operations(id)
    INCLUDE (entity_id,status,expires_at,size) WHERE storage_name IS NOT NULL;
