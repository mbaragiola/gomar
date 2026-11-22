# Propose a document draft through MCP

This optional workflow is off by default. With
`-enable-mcp-document-proposals`, users who can create document drafts and have
a grant for the selected branch can issue a **document proposal key** in Gomar.
The read key and wiki proposal key do not work for this task. A document
proposal key cannot read records, submit wiki pages, approve anything, review a
document, record a payable, or make a payment.

The model submits one typed draft and receives only a proposal number, status
and expiration. The browser shows the proposed fields and the current supplier
snapshot to the same signed-in actor. Approving creates a native draft and
audit entry or changes nothing. Review and accounts payable stay separate.
Rejecting creates no document. No file, OCR, supplier creation or provider
request is part of this workflow.

## API

- `POST /api/v1/auth/mcp-document-proposal-token`: session, exact Origin and
  CSRF; body `{ "entity_id": 1, "outlet_id": 2 }`; returns `id`, `token` and
  `expires_at` (201). `DELETE /api/v1/auth/mcp-document-proposal-token/{id}`
  revokes it with the same browser checks (204).
- `POST /api/v1/mcp/entities/{entity}/outlets/{outlet}/document-draft-proposals`:
  only the document proposal bearer key. Body is
  `{ "request_id": "<lowercase UUIDv4>", "draft": {...} }`.
  Draft fields are in [the adapter contract](MCP-DOCUMENT-DRAFT-ADAPTER.md).
  No role, branch, supplier CUIT, or file belongs in the body. Response:
  `{ "id": 1, "status": "pending", "expires_at": "<RFC3339>" }` (201).
  Within retention, an identical request ID and input returns the original
  proposal (200); changed input with that ID returns 409.
- `GET /api/v1/entities/{entity}/outlets/{outlet}/document-draft-proposals`:
  same-origin session; returns `{ "proposals": [...] }`. Add `/{id}` for
  the detail. It includes the private typed `draft` and its `digest` while
  pending; a terminal response may include `document_id` if approved.
  No CUIT or supplier fingerprint is returned.
- `POST /api/v1/entities/{entity}/outlets/{outlet}/document-draft-proposals/{id}/decision`:
  same-origin session and CSRF; body
  `{ "decision": "approve", "digest": "<preview digest>", "confirm": true }`.
  Use `reject` to discard it. The response is a receipt; only the browser
  sees the resulting `document_id`. A stale or duplicate draft returns 409.
  Expired or revoked proposals cannot be approved (410).

Keys expire after 15 minutes. A pending proposal cannot outlive its key.
Terminal transitions remove the pending preview and supplier fingerprint.
The server prunes expired previews and deletes minimal receipts 24 hours after
the issuing key expires. This is local data in Gomar; backups keep their own
retention. Key and proposal issuance is bounded and shared key limits also
cover the separate wiki workflow.

Gomar does not depend on Turnate billing or any other outside service.
