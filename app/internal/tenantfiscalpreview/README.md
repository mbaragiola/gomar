# Tenant fiscal invoice fixture

This package models a narrow commercial sale and a simulated WSFE result.
It has no ARCA client or runtime caller. No invoice is generated, assigned a
number, printed, or submitted by this package. The only supported input is
one online, fiscal-due sale in the configured entity and outlet, with an
opaque synthetic issuer alias. The fixed class, consumer profile and amount
ceiling narrow the fixture; they are not guidance for a real issuer.

## Boundaries for an integration

- A prebill is not a fiscal document. Keep the Spanish X/no-valid-as-invoice
  marking and archive it separately. Recording one or several clerk-attested
  tender legs is a separate action from submitting an invoice. The operator
  must still see when a fiscal document is due, which may happen before the
  last tender leg. Do not mark fiscal work complete without authorization or
  a documented lawful contingency.
- The eventual caller must read the paid-sale or fiscal-due evidence, active
  grants and issuer configuration from current server records. It must
  authenticate the native human confirmation and persist the reviewed sale
  snapshot. These boundaries are not implemented here. A preview alone does
  not authorize emission and may not be submitted through MCP.
- A durable worker must reserve and reconcile numbers per environment,
  issuer, point of sale and document type. It must not dispatch the same
  approved sale twice or move to another number while its status is
  uncertain. The fixture does not allocate or persist numbers. A provider
  reply must echo the intended document; the full comparison happens in the
  future adapter. A missing lookup result alone does not resolve a timeout.
- Production credentials, ARCA authorization and launch approval are
  separate. An eventual homologation test needs a dedicated test issuer
  and point of sale, controlled certificates and provider egress, and no
  real diners or payments. Corrections to issued invoices require a new
  associated fiscal document, not editing or deleting history.
- When an endpoint or view is added, provide the scoped MCP workflow in the
  same PR under AGENTS.md. Plan limited readiness/status and a native
  confirmation handoff, without taxpayer data, credentials or document
  contents. This package adds no user-facing surface.
- Turnate subscription billing is a different issuer and repository.
  Matching functional rules on retry and proof do not imply shared accounts,
  databases, sequences, or operational dependency.

## Durable fixture only

The opt-in outbox installs its own schema explicitly in a new
`gomar_qa_*` database; app migrations and startup do not know about it.
`scripts/qa-tenant-fiscal-outbox.sh` runs its race test against a private Unix
socket and disposable PostgreSQL cluster without network access.

The fixture freezes an outlet sale per confirmation. Enqueue replays return
the same intent. A database claim reserves one number for one synthetic
issuer and point of sale, with one fixed document class. If a claimant is
lost, its number remains blocked and can be looked up after the lease
expires. Only a matching authorization moves the channel to the next number;
absence, rejection and mismatched results do not. A second database client
can read the persisted request without obtaining another dispatch claim.

There is no worker or transport here. A future dispatcher must use a claim
only once, and must handle an uncertain provider outcome with lookup rather
than calling submit a second time. The package does not send a reply, perform
retries, take custody of credentials or decide a production recovery policy.
