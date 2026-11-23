# Propose an outlet display-name change

The default-off `-enable-mcp-outlet-display-label-proposals` flag adds the
named, separately scoped `outlet_display_label_change` proposal action.
Only a current branch administrator with the native management capability
and a grant for that active outlet can issue and review this proposal.

`gomar_propose_outlet_display_label_change` accepts one exact entity ID and
outlet ID, a UUIDv4 request ID, and `expected_name` and `name`. Use the exact
current display name from `gomar_list_outlets`, not a name alias for the ID.
Both strings must be 1–120 characters with no surrounding whitespace,
control or invisible formatting characters. The new name must differ and
cannot duplicate another outlet's name in that business.

The same browser login compares the current and the proposed name before
confirming. Approval changes only the display name. The outlet ID, state,
permissions, venue slug and public name, POS and stock settings, supplier
data and all business records stay unchanged. Reports that join the current
outlet can show earlier records under the new label; the records keep their
original outlet ID. A second independently approved rename can restore the
old name if it is still free.

Approval verifies the current snapshot under the native entity and outlet
locks, with no write for a stale expectation or competing name claim.
This is a current-state comparison, not a new outlet revision system.
The exact receipt can be retrieved in Gomar; a named MCP bearer retry
returns only proposal status and never applies the change again.
