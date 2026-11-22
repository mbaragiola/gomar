# ADR: Use one scoped proposal session for selected actions

**Status:** Proposed. This ADR does not add credentials, routes or deployment
flags. The first action would be POS product rename. Do not ship before the
existing proposal changes land and pass review on a clean upstream branch.

## Decision

Keep the existing read key and each existing one-purpose proposal key as-is.
Add an optional proposal session for **one signed-in user, one business,
one branch, and a named list of actions that user selects**. It can queue a
proposal, never apply one. It expires in at most 15 minutes, or earlier when
the issuing login expires or is revoked. A signed-in user can end it
immediately. Neither a new release nor a role change can add an action to a
session already issued.

Initially the list contains one eligible action: **Cambiar el nombre de un
producto de caja**. It reuses the
[POS rename adapter](MCP-POS-RENAME-ADAPTER.md). Do not add permissions for
price, active state, stock, sales, payment, file upload, or provider calls.
Do not convert legacy keys or pending proposals to the new format.

## User flow

1. In Gomar, choose a business and branch. Under **Qué puede proponer este chat**,
   select individual named actions. Start with no boxes checked. Disable or
   omit actions that the current role, branch grant or feature flags do not
   allow. Do not offer **Todo**, preset bundles, or a permission editor.
2. Show the chosen actions, the branch, the expiry, and **No aplica cambios.
   Vas a aprobarlos en Gomar**. Creating the session requires the existing
   CSRF check and exact Origin. Give the secret once. Do not store it in
   browser storage or logs; the local client reads it from a private file.
3. When the same user opens **Propuestas**, the browser shows the exact
   branch, product, revision and old/new names. Label the unchanged price
   and active state. Render user text as plain text, not markup. Only
   **Confirmar cambio de nombre** applies it; **Rechazar** makes no change.
   Return just proposal number, status and deadline to chat.
4. Show active sessions by branch, named actions and expiry, never secrets.
   Let the user revoke a session even if they lost the action role. If the
   login expires or is revoked, new submissions and approvals fail. No six
   extra buttons or key files just because more typed actions exist later.

## HTTP and implementation

- Feature flag default off; POS must also be enabled. Add one browser
  credential route, for example
  `POST /api/v1/auth/mcp-proposal-sessions` with
  `{entity_id,outlet_id,actions:["pos_product_rename"]}` and scoped revoke.
  The server rejects duplicates, unknown actions, a zero-length list and
  any action not currently allowed. The list is immutable after issuance.
- The agent uses only
  `POST /api/v1/mcp/entities/{entity}/outlets/{outlet}/pos-rename-proposals`
  with a scoped proposal-session bearer and
  `{request_id,rename:{product_id,expected_revision,name}}`. This dedicated
  route fixes the action. The model cannot submit a method, URL, SQL, role,
  callback or arbitrary payload. Add a dedicated route and typed adapter for
  each future action. The only shared route people may see is the browser
  inbox and decision by proposal ID; its stored immutable action, not a
  submitted action name, selects the adapter.
- Build one internal typed Go proposal workflow around the existing
  credential, scope, clock and browser invariants. Pass a fixed adapter at
  registration. Share bounded decode, scoped replay, preview digest, actor
  checks, revoke/expire, terminal state and receipt; do not share the native
  mutation directly or bypass its validation. Start with the new action;
  existing code can adopt common internals later under parity tests.
- Bind the session to the issuing authenticated login using its server-side
  session hash, user, business, branch and current password digest. Store
  only a hash of the new bearer. On submit, lock and validate the current
  origin session and action grant. For browser approval, require the same
  authenticated actor and issuing login, exact Origin and existing CSRF;
  proposal keys never count as a session. Do not hold an FK that cascades away
  audit history when login ends. Reject other actors, branches and logins
  instead of transferring the proposal. Same actor can explicitly revoke
  from another current same-origin login.
- In each approval transaction, lock and reload session/key, pending proposal
  and current permissions. Decode the stored typed snapshot, compare the
  reviewed digest and recheck expiry and the native product revision. Use
  the native outlet write lock before any weaker outlet scope lock, honoring
  the adapter's lock order. Capture the immutable native product version
  and an approval receipt with the actor in **one transaction**. POS version
  history has no actor field, so the receipt must carry that linkage.
- Continue the existing request-ID contract: uniqueness includes actor, scope
  and action. An identical retry returns the existing receipt. A different
  normalized request under that ID returns 409. Keep a normalized input digest
  separate from the reviewed snapshot digest so a retry doesn't re-read or
  silently change a preview. After a terminal decision clear private payload
  bytes; expose no product or receipt identifiers to the model beyond the
  proposal number. Expire pending previews; retain only minimal receipts for
  the same 24-hour window as current proposals.

## Additive storage and limits

Add new tables in the next unused migration after the prior proposal
migrations. Leave their SQL checksums alone. Suggested shape:
`mcp_proposal_sessions` (scope, issuing login hash, bearer hash, time,
revocation), immutable `mcp_proposal_session_actions` (fixed action grant),
and a small proposal envelope with a purpose-specific typed payload and
typed result reference. Bind proposal scope and action to the issued grant
with foreign keys or equivalent server-enforced integrity. Do not turn a
database text string into a callable mutator. Preserve legacy tokens,
pending snapshots, revocation, retention and existing routes with no bulk
conversion.

Do not use the new row type to evade the existing limits: four active
proposal credentials across both formats, at most three active grants for
any one action, and 48 issues per actor in the existing daily window. A new
session counts once against the total and once per explicitly selected
action. Enforce counts in the same actor-serialized transaction as issuance;
revoked or expired credentials don't count as active. Keep pending and
recent proposal limits at least as restrictive as the corresponding
one-purpose workflow. Read keys remain independent and cannot submit.

## Release gates

- Fresh local PostgreSQL plus rehearsal from the preceding migration with
  synthetic legacy keys and proposals; prove old paths, decisions and
  revocations still work without editing any old rows.
- Compare with the native POS catalog path: same branch lock, same name
  validation, expected revision and native append-only version; rename
  changes nothing else. Retain an old sales item at its original version and
  prove the price, active state, stock and recorded sale are unchanged.
- Run focused cases for nothing before approval, disabled feature, duplicate
  action selection, missing action grant, wrong origin/CSRF, wrong
  user/login/business/branch, both kinds of expired session, role/grant loss,
  credential revocation, all quota combinations, wrong digest, stale native
  edit, two approvals, identical/reused UUID, rejection and injected receipt
  failure. The last case must prove no native revision survives rollback.
- Exercise only synthetic records through packaged MCP, then through the
  local browser. Check the visible action checklist, deferred preview and
  one approval. Record real Codex/Claude discovery, calls and natural
  language separately. Don't call the feature production-ready based on
  schema, unit tests, or a mock browser alone.
