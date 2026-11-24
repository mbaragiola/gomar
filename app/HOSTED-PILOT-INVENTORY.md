# Hosted pilot: delivery read slice

No hosted pilot starts from this code. The route inventory test observes the
same explicit router registrations used for self-hosted Gomar. Its classifications
are test data, not authorization or deployment settings. A newly registered
method or template is outside the candidate unless explicitly classified.
The test samples default and selected feature configurations; it does not
enumerate every toggle combination.

Start with business and outlet discovery and the delivery inbox: one native
inbox and the packaged MCP tools `gomar_list_recent_delivery` and
`gomar_delivery_status_summary`. Both clients must keep the ordinary role and
branch checks and check current hosted eligibility for the exact branch in the
same read transaction. Discovery must show only admitted scope. The native
inbox includes the provider label, external order ID and items. The MCP
recent-order tool includes the provider label but not the external ID or items.
These reads do not return raw provider payloads, credentials or customer contacts.

Health and session setup need their own narrow rules. The current Vue catch-all
also serves static assets, and the router has a separate fallback for unfinished
API routes. Neither fallback is proof of an allowed route. A pilot must select an
exact shell and asset set, exclude the POS shell and service worker, and handle
HTTP methods explicitly, including ServeMux's automatic HEAD match for GET
patterns. Do not admit admin/recovery, signup, employee or MCP credential
management, public reservations, provider ingestion, offline writes, or
proposal and approval workflows. A route inventory cannot cover jobs, other
listeners, or internal calls; review those separately before starting a pilot.

A hosted app route filter alone does not change the packaged MCP tool list.
Before start-up support, make the tools shown by the pilot's packaged client
match the callable tools, and enforce the limit on the server independently of
the client. Do not treat the existing full self-host catalog as a pilot catalog.

Before enabling any pilot, run real Chrome and packaged MCP against a fresh
local PostgreSQL fixture. Give the same account access to two distinct paid
scopes, which may share a legal entity. Expire one scope and check that
discovery, direct inbox reads and both tools deny its branches while the
other scope still works. Do not expire one branch within a single lease:
a lease covers the whole approved branch set. Check direct calls to
unlisted tools, missing roles and cross-business access. A denied browser
refresh must clear prior inbox data. Confirm that excluded UI, background
work, and direct HTTP requests do not fall through, and run the same existing
workflows on self-hosted Gomar to verify it still behaves as before. This
inventory does not prove any of those controls by itself.

## Isolated paired test

The PostgreSQL delivery test runs a second, test-only HTTP listener. It has
only discovery and inbox GET routes and does not use the app router, Vue shell,
static assets or setup endpoints. A separate synthetic setup listener issues
an ordinary provider-session cookie and scoped bearer keys beforehand. This
does not test the main router's provider-only or managed employee request
policy. No operational write route is exposed; authentication can still
revoke a stale session. The test connects an actual Go SDK client to a
four-tool SDK catalog using the same registrations as the packaged client,
and those tools call the isolated HTTP listener. Direct calls to unlisted tools
and HTTP routes are denied, and one expired paid scope does not block another.

This proves the paired read path in a fixture, not a hosted pilot binary or
browser screen. Existing Chrome and packaged MCP checks still cover the
unchanged app and package. There is no production startup option for this
fixture, and it does not replace the acceptance checks above.
