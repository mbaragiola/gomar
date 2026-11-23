# Prepare one start for a disabled reservation venue

The default-off `-enable-mcp-reservation-slot-add-proposals` flag adds
one named proposal action, `reservation_slot_add`. It is separate from
public reservations and does not enable them.

A current administrator with a grant for the exact outlet can issue a
named key. `gomar_propose_reservation_slot_add` accepts the entity, outlet,
a UUIDv4 request ID, and exactly one canonical UTC start such as
`2030-05-21T01:30:00Z` (Buenos Aires: May 20, 22:30). The input must be UTC
RFC3339 with literal `Z`, seconds `00`, and no fraction. The server uses
the native Buenos Aires grid (every 90 minutes from local midnight) and
accepts only future starts on today's local date through local day +60.
The proposal expires at the earlier of the key's deadline or slot start.

The issuing browser login reviews the exact local date and time. To
approve, explicitly acknowledge that Gomar cannot remove this slot and
that it can become available only after someone enables the venue
separately and a suitable table is available. The venue must already
exist and still be disabled at approval. A competing enable, duplicate,
or elapsed start is refused. Gomar rechecks native time and venue state
under the same outlet lock as native settings and slot changes.

Approval creates one slot. It does not enable reservations, list or
modify other slots, add tables, change existing requests or contacts,
book anything, or send notices. An exact retry from the issuing login
returns the original proposal receipt, never an extra slot.
