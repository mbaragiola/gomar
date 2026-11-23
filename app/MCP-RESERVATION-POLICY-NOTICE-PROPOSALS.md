# Scoped reservation cancellation notice proposals

Enable this optional action with `-enable-mcp-reservation-policy-notice-proposals`.
A current reservation administrator with an active outlet grant can issue a
named `reservation_cancellation_policy_change` session for one existing venue.
Call `gomar_propose_reservation_policy_notice` with exact business and outlet
IDs, a lowercase UUIDv4 request ID, and `policy_change.notice_hours` as a whole
number from 1 to 72. The venue must be disabled. The tool returns a generic
proposal receipt, not either text or a policy digest.

The issuing browser shows the existing text and the exact proposed Spanish
notice. Confirm that it is informational and only applies to new requests
after you separately enable the venue. The proposal changes only the
cancellation policy field; it cannot enable a venue, send messages, change
availability, cancel bookings, charge a fee, or impose a penalty. Use the
native venue settings for custom wording.

The server compares the current disabled state and policy with the reviewed
snapshot under the same outlet lock as venue settings and booking. It does
not track every intermediate change. An identical text stays a generic pending
receipt until the browser reviews it; it cannot be applied.

Existing requests keep the server wording recorded at submission. The
snapshot is not proof of what a guest saw or accepted. No policy change
rewrites or enforces those stored snapshots.
