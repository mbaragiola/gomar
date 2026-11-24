# Native owner consent: durable internal contract

Migration 040 adds empty, append-only PostgreSQL tables for internal
designations, recovery approvals/revocations, hashed previews, and
receipts. It follows 039 without a backfill or change to ordinary login,
MCP, or existing access. This is not a downgrade procedure.

No signup route, flag, UI, CLI, MCP tool, transfer, Billing adapter, or
entitlement check calls this contract. The synthetic provider test is
the only mount. It signs in through Gomar's ordinary provider handlers
and separately through the existing protected recovery realm. The
operator is resolved and locked from that recovery session, not a
browser-supplied ID. The claimant is a verified ordinary provider user,
never an employee, administrator cookie, or an identity inferred from
email. The fixture supplies the otherwise unimplemented trusted
claimant mapping.

The intent fixes its business and hosted audience. Each new designation
revision has a new opaque private request reference and canonical exact
outlet set. A revision can receive one independent recovery approval.
Revoking it is a separate immutable entry; it cannot be reopened by
retrying the approval. Before acceptance a newer designation immediately
supersedes the previous revision, even if the newer one lacks approval.
After acceptance the intent is permanently consumed. A different
claimant, scope, or fresh approval requires a newly designated intent;
revocation does not rearm the old one.

The positive path locks the actor, exact provider session, intent and
preview in that order using Read Committed. Explicit confirmation can
only use a live preview from the original session and a live exact
recovery approval. One receipt insert consumes the preview and its
intent, not just that revision. Raw cookies, session material, provider identities, payer and
financial fields never belong in the receipt. Only hashes are persisted
for browser sessions and challenges. A receipt reference cannot
authenticate its presenter.

The historical receipt remains immutable. A separate exact lookup
reports whether the approval, designation, original session and receipt
are still current, at the fresh authorization check time. Revocation,
supersession, expiry, or a different session does not erase history and
never grants current use. This local status, or a successful consent,
is not payment or entitlement evidence.

Before wiring anything, verify cross-system service identity/audience,
trusted hosted scope mapping and exact owner/intent/request binding,
opaque reference handling, expiry and replay, and how authorization is
rechecked if approval or payment is reversed during delivery. Separately
design the staff/owner UI, retention of expired previews, and rollout.
Neither signup nor any change to entities, outlets, memberships, or
grants is provided here.
