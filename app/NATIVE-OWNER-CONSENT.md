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

## Dormant first-owner install

Migration 041 adds empty immutable tables for a locally recovery-approved
organization draft, one pending install and intended administration
assignment, and its exact pending branches. The install refers to an
existing 040 consent; a one-time opaque receipt is history, not a
credential. It never creates an operational entity, outlet, membership
or grant. Inactive flags on operational rows would not be enough: the
ordinary catalog can edit outlet activation, independent of this
contract. The dormant tables have no activation path. This migration
does not claim it is safe to boot an older executable on a newer schema.

The unregistered install seam accepts only the designated claimant's
original active provider session, exact current native consent and
recovery approval, a private local draft matching the owner-reviewed
display, and an independently authenticated, short-lived Billing observation for a new server-minted
nonce. The synthetic verifier binds the expected issuer, recipient,
purpose and schema and exact audience, intent, business, request,
claimant, initiator, branches, revision and consent. The minimal
Billing result is not itself authentication or a complete binding.
The verifier is called outside native transactions; the original
request, current native state, timestamp and deadline are checked again
before a single native commit. CUIT, legal or display organization names,
branch labels, display or browser digests, and credentials are not sent
to this verifier or returned in the install receipt. Existing operational CUITs are refused, not
adopted; the short native check does not reserve a CUIT in ordinary
operations after the dormant commit.

The local fixture mounts the seam only for synthetic HTTP tests with
real PostgreSQL. An exact retry can read immutable dormant history
without asking Billing for a second observation or renewing access.
Even a reversal after the private check cannot create operational
access here. No authenticated inter-service transport, cross-database
atomicity, request mapping in a live system, checkout, public route,
MCP workflow, or activation mechanism is provided.

### Disposable staged-activation proof

The separate `staged_activation_proof` provider subtest uses real
operational table constraints in a disposable database. It creates one
new inactive entity, only the exact approved branches, and one inactive
administration membership and its exact grants in one transaction with
an immutable staged receipt. It verifies the exact pending branch
references **and names** against the approved draft; existing CUITs
are not adopted. A distinct synthetic, fresh staging authority is
required, never the first-install observation or an old receipt.

Quarantine triggers and the motion that creates these rows exist
only in the test binary and its database. An ordinary recovery
catalog action cannot enable the fixture rows, and regular application
and MCP credential entry points give them no access. The fixture never
enables any operational usage. There is no registered staging/activation
route or migration, runtime hosted authorization gate, proof of packaged
MCP activation/suspension, tenancy or RLS choice, real provider, or
cross-store atomicity.

## Native customer review in the isolated fixture

The local HTTPS fixture can explicitly configure **Revisá el alta** with
an exact loopback origin and hosted audience. With no configuration,
a bare enable bit, non-loopback authority, or no single current plan for
the provider account and audience, there is no customer signup workflow.
No command-line flag, environment setting, or default app wiring enables it.

The browser supplies no actor, intent, business, branch scope, payer
email, role, or private request reference. PostgreSQL resolves one exact
native designation and recovery-authored display after checking the
user's ordinary verified provider session. The immutable
designation and freshly resolved labels are bound to a separate hash
domain on the actual 040 preview. A stale scope or changed label cannot
use the old challenge, and a new session needs a new review. Only a
same-origin, CSRF-checked, closed-shape JSON confirmation by the original
session records the 040 receipt. Questions about payment, access and
activation stay separate. Reviewing, canceling, or confirming here
never creates a 041 approved draft, dormant install, operational role
or MCP credential.

### Protected source of the display

Migration 043 starts with no authored plans. The unregistered recovery
operation can record one immutable organization display and exact
branch-label set for an active approved designation revision, through
the existing protected recovery session. It never infers a match by
email or CUIT or writes legal data. Changing the display requires a
new applicable designation revision and recovery approval before
consent; an accepted intent cannot be reused.

At review or confirmation, SQL starts from the verified provider actor
and the configured audience. Zero or multiple eligible plans are denied;
expired, revoked, superseded, or consumed plans are not alternatives.
The actor/session and selected intent stay pinned through the preview
or confirmation transaction. A competing author waits on the same
claimant before acquiring the intent lock. Plans and their approval
history survive an application restart without a callback, backfill,
runtime writer, or new registered route.

### Durable display evidence at the recovery handoff

Migration 042 adds one nullable, immutable version-1 display digest to
consent receipts and a separate local display-organization field to
drafts. The digest binds the exact native actor, audience, intent,
business, request, revision, display organization and approved branch
references paired with their exact labels, in the same order the browser
shows them. Only explicit signup confirmation records it in the same
transaction as the consent. Legacy receipts and drafts are not
backfilled; exact history stays readable but cannot create a new draft
or pending install without this evidence.

The protected recovery operator still vets the legal entity name and
CUIT independently. The owner approved the display organization and
branch labels, not the legal identity or CUIT; a different legal name
is permitted only in its separate field. Recovery draft creation and
both native preflight and post-verifier checks for a fresh dormant
install compare the exact display while holding the native intent lock.
Changing a label or swapping two branch labels is a mismatch. No label
other than the intended browser display, legal data, or digest leaves
the native operation. Neither a draft nor pending history grants access.

The synthetic fixture proves real local HTTP and browser behavior, not
a live provider, public signup, payment, entitlement,
deployment, or operational access. Any live rollout still needs an
approved source and mapping for real customer plans.
