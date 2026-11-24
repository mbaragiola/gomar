# Isolated hosted discovery, timing and barcode proof

This is only an explicitly injected, narrow hosted fixture. There is no
startup flag, producer, live eligibility connection, or deployment
setting. Existing and self-hosted apps keep their normal behavior when
the discovery fixture is absent.

Migration 044 starts without classifying any existing entity. An entity
may be explicitly self-hosted or have paid discovery scopes. The opaque
paid business and branch references are separate from legal names,
CUITs, entities, and outlets. Each paid branch has one immutable exact
outlet mapping, enforced against the outlet and its owning entity. This
is not a one-subscription-per-CUIT or one-subscription-per-entity rule.

A paid scope specifies its complete canonical branch set. In discovery
the latest epoch is selected before checking its validity. Exactly one
eligible claim, its complete mapped scope, and a verified interval of
at most five seconds are required. A disputed, future, expired,
suspended, or unknown latest claim cannot fall back to prior positives.
A purported newer positive checked no later than a recorded suspension,
unknown, or earlier epoch dispute cannot restore discovery. An exact
copy is not a new record. No writer or observation source is registered
by the app. These synthetic observations have no live Billing authority.

The same check intersects the mapped, current scope with active local
outlets and existing actor membership and grants. A paid entity's name
and CUIT are returned only if at least one currently eligible mapped
active outlet is already granted. An exact outlet MCP credential needs
its own exact outlet, not another grant. Unclassified entities and
unmapped outlets are hidden in this fixture. An explicitly self-hosted
entity retains the existing native entity capability projection and
scoped read-role requirements, including for proposal-session management.
Discovery neither creates capabilities nor admits a proposal-only
membership to outlet or MCP reads. No opaque paid references or
verification details are returned.

The read portion covers only the existing native entity/outlet lists,
exact own Caja sale timing, and their corresponding packaged MCP reads.
The barcode lane below is its sole write exception. There is no shared
production mode or hosted access guarantee for other POS operations,
MCP tools, proposal types, administration, public menus/reservations,
background work, branch expansion, or offline Caja.

The existing exact Caja sale-time lookup is also included. Both the
native **Horarios de la venta de Caja** handoff and
`gomar_read_pos_sale_time` still need the original cashier, a selling
role, the specified active granted outlet and the known sale reference.
MCP still needs a token pinned to that exact outlet. The current mapped
outlet check and timing receipt are filtered in one SQL statement; loss
of that paid scope returns the same 404 as an unknown sale, with no
clock or handoff data. This does not change device-held receipts, other
POS reads, table finalization, sales, payments, or monthly reports.

## Internal observation application fixture

The app can exercise the append procedure using a verifier injected only
in the isolated fixture. There is no operator or user action, browser
endpoint, MCP operation, polling loop, callback, or provider. The
verifier is responsible for authenticating the source and correlating a
complete response before the local transaction. The helper checks one
configured issuer and audience, the dedicated hosted eligibility purpose,
and an exact opaque business plus ordered, unique complete branch set.
Owner consent, install history, payment diagnostics, provider revisions,
and an asserted body do not provide this authority.

The helper verifies outside the transaction, then locks the exact paid
scope in an explicit Read Committed transaction. It independently checks
the immutable declared and actual mapped branch sets. A new positive must
have a higher epoch and checked-at later than all known evidence; its
original bounded window must still hold after the lock and at the write.
The helper does not re-stamp or extend a window. Exact replay is history
only. Authenticated nonfuture adverse or conflicting observations may
be recorded as history after their original window. A conflict disables
that epoch. When delayed adverse evidence or a dispute's timestamp is not
earlier than a higher positive's check, that positive cannot authorize a
read. A later independently verified positive must be chronologically
newer. If verification fails, it cannot renew a lease; a previously
recorded valid lease can still last until expiry or a durable denial.
This does not guarantee immediate revocation across services.

There is no migration in this slice. The existing history stores exact
scope, source epoch, state, and original times, but not cryptographic
proof, verified issuer/audience, receipt provenance, or per-source history.
The isolated fixture assumes one configured trust source per database.
Do not rotate or mix sources, trust existing raw rows as authenticated,
or turn this into a production admission path without a separate durable
provenance, migration, and transport decision.

## Existing product barcode only

The injected fixture can set or clear one existing product's barcode
using the current native endpoint or the existing barcode proposal and
human browser decision. It does not grant either path a new right. The
native transaction pins its existing authenticated actor and exact
ordinary login first. All paths keep existing entity, role, outlet,
credential, proposal-session, digest and CSRF checks. Only the barcode
lane adds a lock on its exact mapped paid scope, after its operational
locks. It uses `FOR SHARE`, conflicting with the observation appender's
`FOR UPDATE`; the appender never takes operational locks. Read Committed
rechecks #044 eligibility and the full map after the lock, then checks
the same lease's original deadline again before commit or receipt.

A suspended, unmapped or expired paid scope returns a generic denial
for native save and barcode proposal preparation, replay, review and
decision. A failed transaction leaves the old barcode, product revision,
proposal and receipt intact. A writer already holding the scope can
finish before the subsequent observed suspension if its lease is still
current; this is not a cross-service or instant revocation claim.
Explicit self-host and the unconfigured app retain their existing
policy. Other catalog actions, barcode lookup, named key issue/revoke,
background pruning, payment, sales, stock and offline paths are not
changed. No real transport, permissions, rollout or new schema is added.
