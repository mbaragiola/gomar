# Scoped draft document printed point-of-sale proposals

This optional workflow corrects only the five-digit point-of-sale prefix
printed on an existing unreviewed, unposted draft. It accepts the native
range 1 to 99,999; zero or noncanonical legacy references must be handled
in the native form. This is not a store or register change. It preserves the
printed number, document letter/type, branch, issuer, supplier, currency,
dates, amounts, notes, files, and review and posting state.

Enable explicitly with `-enable-mcp-document-point-of-sale-proposals`. An
administrator or purchasing user with `manage_draft_documents` and the
current grant for the exact active branch can issue a distinct named
`document_draft_point_of_sale_change` session. Configure that action using
`--proposal-session-action` with its private named credential, and call
`gomar_propose_document_point_of_sale_change` with the exact business and
outlet IDs, the draft's `document_id` and positive `expected_revision`, the
integer `point_of_sale`, and a lowercase UUIDv4 request ID. Existing
number-only credentials and requests are unchanged and cannot use this
action.

The calling tool sees only a proposal receipt. The issuing browser reviews
both formatted references and confirms or rejects separately. The update
changes one revision and adds one native audit entry. Unchanged references
cannot be applied. The existing business-wide document identity constraint
still applies, including deleted records. A generic approval conflict
never identifies another document, issuer, or branch.
