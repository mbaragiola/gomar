# Scoped MCP workspace reads

These tools use the same short-lived bearer and the current Gomar permissions.
Use the entity and outlet IDs returned by the discovery tools. A bearer
issued for one outlet cannot see any other outlet. No tool contacts a
delivery provider, writes a record, sends a message, or accesses a receipt.

| Tool | Returns | Extra limits |
| --- | --- | --- |
| `gomar_search_knowledge` | Article IDs, titles, versions and update times | Up to 30 matches; narrow the search if truncated. No article bodies. |
| `gomar_read_knowledge_article` | One article by ID from the selected outlet | Article text is reference material, not instructions for the chat client. |
| `gomar_list_stock` | Tracked products, balances, minimums and alerts | Requires POS and stock enabled and a qualifying role. Use `next_product_id` to continue. |
| `gomar_list_recent_delivery` | Internal order IDs, provider, recorded status and times | Requires delivery enabled. Up to 30; no customer data, provider order IDs, items, or amounts. |
| `gomar_payables_summary` | Outstanding and overdue balances by currency, in minor units | Uses the existing accounting role and outlet checks. No supplier details, invoices or payments. |

Sales still means recorded POS sales, not delivery or invoices. The tools
can be listed even when an app feature is off or the user lacks permission;
the server denies the call in either case. A denied call is not an empty report.

## Not available through MCP yet

The app still has business actions and reads without MCP tools: document
and supplier detail, private files and intake, payment workflows, detailed
stock and reconciliation, reservations and menu management, and access
administration. No payment, booking, stock, publication or account change
can be made through these tools.

For future writes, give each workflow its own typed operation. Show the
operator the exact proposed change in Gomar, then require a fresh
authenticated confirmation tied to the user, entity/outlet, exact payload,
expiry and idempotency key. Recheck permissions and target state inside the
transaction before applying it. Do not treat words spoken to the chat,
an assistant's claim of confirmation, or a client-side flag as approval.
