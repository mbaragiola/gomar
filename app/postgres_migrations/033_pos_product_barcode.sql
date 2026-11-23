-- A product code is an exact string and unique among all products at a
-- branch, including inactive products. Historic product and sale rows are untouched.
ALTER TABLE pos_products ADD COLUMN barcode TEXT COLLATE "C"
    CHECK (barcode IS NULL OR (octet_length(barcode) BETWEEN 1 AND 80 AND barcode !~ '[^!-~]'));
CREATE UNIQUE INDEX pos_products_outlet_barcode ON pos_products(outlet_id,barcode) WHERE barcode IS NOT NULL;

ALTER TABLE mcp_proposal_session_actions DROP CONSTRAINT mcp_proposal_session_actions_action_check;
ALTER TABLE mcp_proposal_session_actions ADD CONSTRAINT mcp_proposal_session_actions_action_check
    CHECK(action IN ('pos_product_rename','document_draft_review','reconciliation_draft_create','document_draft_edit','stock_waste_record','stock_physical_count','pos_product_price_change','pos_product_availability_change','reservation_confirm','reservation_cancel','pos_product_create','stock_minimum_change','supplier_legal_name_change','stock_tracking_change','stock_product_setup','reservation_table_add','reservation_slot_add','outlet_display_label_change','document_draft_number_change','reservation_cancellation_policy_change','document_draft_point_of_sale_change','pos_product_barcode_change'));

ALTER TABLE mcp_pos_catalog_proposals DROP CONSTRAINT mcp_pos_catalog_proposals_action_check;
ALTER TABLE mcp_pos_catalog_proposals ADD CONSTRAINT mcp_pos_catalog_proposals_action_check
    CHECK(action IN ('pos_product_price_change','pos_product_availability_change','pos_product_barcode_change'));
