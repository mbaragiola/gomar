# Document draft proposals: server adapter

This patch does not add a route, credential, proposal store, chat tool, or UI.
It is independent of the wiki proposal branch. Keep the public Gomar workflow
self-hosted; it has no dependency on Turnate billing or any outside provider.

## Typed input

Accept only these document fields: `supplier_id`, `type`, `letter`,
`point_of_sale`, `number`, `issue_date`, optional `due_date`, `currency`,
optional `net_minor` and `tax_minor`, and required `total_minor`.
The three amounts are integers in centavos, between 0 and 99,999,999,999,999.
Dates use YYYY-MM-DD. Currency is ARS or USD. The existing draft writer remains
the authority on validity; the adapter does not infer taxes or calculate totals.
No CUIT, free-text description, receipts, files, or OCR enters this interface.

## Hooks for the guarded workflow

- The future endpoint chooses purpose `document_draft_create.v1` itself. The
  authenticated, purpose-specific key supplies actor, entity and outlet; the
  model cannot choose them in the payload. Other keys cannot submit.
- `decodeDocumentDraftProposal` parses the bounded typed document payload.
  `prepareDocumentDraftProposal` rechecks draft permissions and exact current
  outlet grant, looks up an active supplier in the same entity, and checks the
  native validation rules. It makes no domain writes.
- Store the resulting typed payload privately with its original digest. Keep
  preview output limited to `Preview`, never the supplier fingerprint or CUIT.
  The digest binds the purpose, scope, canonical fields and supplier snapshot.
  Keep a second idempotency digest of the normalized input and trusted scope
  alone. An unchanged retry returns the existing receipt even if the supplier
  changed; changed input with the same request ID is a conflict. Check
  idempotency before preparing against current supplier data.
- On explicit same-actor, same-origin browser approval, require the digest the
  browser displayed. Call `applyDocumentDraftProposal` and write the approval
  receipt in the **same transaction**. The adapter rechecks the browser
  session, role, outlet, digest and supplier under the native entity lock.
  Acquire that entity in write mode **before** shared entity/scope locks;
  do not try to upgrade a shared lock at approval. Keep the terminal
  receipt, discard the proposal details, and return no private fields to chat.
- Supplier changes, inactive suppliers and duplicate identities reject
  approval. A forced audit failure must roll back the document and receipt.
  The approval engine must handle revocation, expiry, token purpose, bounded
  retention, and scoped idempotency. The adapter intentionally has no route
  that can skip those checks.

A successful approval creates **only** a document at `draft`, revision 1, and
its normal audit entry. Existing browser **review** is a separate action,
with its own role, reason and expected revision. Posting to accounts payable
and recording payments stay separate and are not available from this adapter.
