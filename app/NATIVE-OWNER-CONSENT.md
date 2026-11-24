# Native owner consent: isolated contract only

This exercise does not register signup routes, flags, a schema migration,
a CLI command, an MCP tool, or a commercial integration. The Go seam has
no production caller. The HTTP mount, designation source, and consent
store exist only in the disposable PostgreSQL test. The fixture records
consent and never creates or enables a business, location, role, grant,
or entitlement.

The test stages the designated owner using the exact internal Gomar user
from real synthetic provider sign-in, not an issuer's email, roles, or a
browser-supplied user ID. A same-origin native preview shows only a
server-owned opaque intent, business, outlet set and revision. The explicit
POST that accepts them needs the same active, ordinary provider session and
CSRF. A new session for the same person cannot accept the old preview: the
person must open and accept a new one. Consent locks the actor before
rechecking the session, in the same order as provider session rotation.
The fixture holds session deletion briefly to exercise this overlap, and
consumes at most one challenge per intent. Consent is not evidence of payment.

Before wiring any route or provisioning access, independently verify the
cross-system delivery of both the designation and the commercial
authorization. The recipient must be able to verify the service identity
and intended audience, exact immutable owner/intent/business/outlet
binding, exact authorized revision, expiry, replay behavior, and how a
revocation or an ambiguous delivery prevents provisioning. Keep provider
claims, browser credentials, payment or pricing data, and private service
identifiers out of browser and MCP responses. This test does not prove
that transport, a live provider, or an entitlement is ready.
