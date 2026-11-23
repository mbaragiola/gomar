# Prepare one table for a disabled reservation venue

The default-off `-enable-mcp-reservation-table-add-proposals` flag adds
one named action, `reservation_table_add`. It is separate from any
public reservations setting.

An administrator with a current grant for an exact outlet can issue a
named key. `gomar_propose_reservation_table_add` accepts the entity,
outlet, table number (1–9999), integer capacity (1–12), and a UUIDv4 request
ID. Gomar stores the canonical label `Mesa N`, not a free-text name.
The outlet must already have a disabled reservation venue. Gomar refuses
an existing number in that venue, including differently padded or
capitalized equivalents.

The issuing browser login sees the exact table and capacity, confirms
the venue remains disabled, and acknowledges that Gomar currently
cannot remove the table. Approval takes the same outlet lock as the
native reservations settings and refuses the request if the venue is
enabled by then or the number is taken. It does not open reservations
or add any shifts, and it never confirms, cancels or edits requests,
touches guest contacts, or sends a notification.

Someone must separately enable the venue through native settings.
At that point, the table can make already configured future times
available to parties who fit, or change which table a later manual
confirmation would select. There is no automatic booking or reassignment.
A retry with identical contents and the original issuing login returns
the existing proposal receipt; it cannot add another table.
