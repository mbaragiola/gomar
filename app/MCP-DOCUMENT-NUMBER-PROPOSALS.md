# Scoped draft document number proposals

This optional workflow corrects only the printed number (1 to 99,999,999)
on one existing unreviewed, unposted draft. It does not change the point of
sale, supplier, issuer, document type or letter, dates, amounts, files,
outlet, or review state. It creates no payable or payment.

Enable explicitly with `-enable-mcp-document-number-proposals`. An administrator
or purchasing user with native `manage_draft_documents` and a current grant
for the selected active outlet can issue a named
`document_draft_number_change` proposal session in Gomar. In the packaged
stdio server use `--proposal-session-action=document_draft_number_change`
and the corresponding private token file. Call
`gomar_propose_document_number_change` with the exact entity, outlet,
`document_id` and positive `expected_revision` from `gomar_read_document`,
the integer `number`, and a lowercase version-4 `request_id`. This returns
only a proposal receipt, not the document record or an applied result.

The original signed-in browser reviews both full printed references, the
outlet and revision, then confirms or rejects in a separate step. Approval
rechecks the draft, session and grant, updates the number and revision, and
records a fixed native audit reason in one transaction. A conflicting existing
number within the same native document identity, stale revision, or lost scope
does not apply a change. The proposal response does not identify another
document or outlet. Open the native draft to verify any conflict.
