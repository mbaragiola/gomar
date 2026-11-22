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
| `gomar_list_payables` / `gomar_read_payable` | Internal payable ID, local dates, status, amount and balance by currency | 30 per page. No supplier or document identity. Excludes rows affected by another outlet. |
| `gomar_list_payable_movements` | One payable’s ledger: type, Buenos Aires date (if recorded), signed change and resulting balance in minor units | Same finance role and outlet checks plus foreign reversal checks. Up to 30 in ledger order; reuse `next_cursor` only for the same payable. No supplier, document, payment or receipt identity. |
| `gomar_list_recorded_payments` / `gomar_read_recorded_payment` | Internal payment record ID, local dates, status and amount | 30 per page. Excludes cross-outlet settlements and linked reversals, with no method, accounts, notes or receipts. Not confirmation that money moved. |
| `gomar_document_totals` | Count and net document totals by currency for the selected outlet | All non-deleted documents; credits subtract; exact signed strings in minor units. |
| `gomar_list_documents` | Document metadata for the selected outlet | No attachments or tax identifiers. Up to 30 per page. |
| `gomar_read_document` | One document’s status, revision, reference, dates and accounting totals | No supplier data, notes, audit, or attachments. |
| `gomar_list_suppliers` | Active supplier names and IDs for the business | Requires an authorized selected outlet. No contact or tax details. Up to 30 per page. |
| `gomar_payment_status_summary` | Recorded payment counts by status | No amounts, account details or receipts. |
| `gomar_reconciliation_status_summary` | Accessible reconciliation counts by status | Do not sum across outlets. No movements, accounts or files. |
| `gomar_list_reconciliation_runs` | State, revision, dates and counts for eligible runs | Up to 30 per page by ID. Requires grants to every participating outlet; an outlet-scoped key sees only runs for that outlet alone. |
| `gomar_read_reconciliation_run` | The same metadata for one run by ID | Same scope as the list; no bank details, source labels, CSV or individual rows. |
| `gomar_reservation_status` | Daily reservation and guest counts by status | One Buenos Aires date. No identifying details. |
| `gomar_list_reservation_requests` / `gomar_read_reservation_request` | Request ID, status, party size and local start time | Administration only. Select a Buenos Aires date and one granted outlet; use `next_id` on the same date and outlet for pages of 30. No guest or table data, and no changes. |
| `gomar_published_menu` | Whether a menu is published and its file metadata | No file or download link. |
| `gomar_pos_catalog` | Products and ARS prices in minor units | Up to 30 per page. |
| `gomar_stock_movements` | Counts and net units by movement type | One product and up to 31 Buenos Aires days, with no actor or order IDs. |
| `gomar_list_stock_movements` | Individual movement type, signed change and Buenos Aires timestamp | One authorized product, 1 to 31 days, up to 30 rows per page. No actor, movement ID, order or notes. |

In payable movement history, `post` adds the payable, `payment` subtracts
only the amount applied to that payable (not the whole payment), and
`reversal` restores the reversed share. Drafts do not change the ledger.
There are no void entries or status forecasts; payment and reversal refer
to internal records, not bank confirmation. Pages do not share a frozen
snapshot; later changes may appear, and eligibility is checked again.
The order is ledger recording order, not a sort by date.

Sales still means recorded POS sales, not delivery or invoices. The tools
can be listed even when an app feature is off or the user lacks permission;
the server denies the call in either case. A denied call is not an empty report.
The detailed payable and payment lists cover only records that do not depend
on another outlet. Do not use them to reconstruct the broader accounting
summaries or a bank statement. A reservation ID is a receipt reference,
not an authorization credential. Never rely on it alone for a public
reservation lookup or change.

## Not available through MCP yet

See the [coverage matrix](MCP-COVERAGE.md) for work still needed. No
payment, booking, stock, publication or account change can be made through
these tools.

For future writes, give each workflow its own typed operation. Show the
operator the exact proposed change in Gomar, then require a fresh
authenticated confirmation tied to the user, entity/outlet, exact payload,
expiry and idempotency key. Recheck permissions and target state inside the
transaction before applying it. Do not treat words spoken to the chat,
an assistant's claim of confirmation, or a client-side flag as approval.

`gomar_delivery_status_summary` counts received, accepted, and cancelled orders
in the latest 100 records of the selected branch inbox. It reads Gomar only
and returns no individual orders, items, or customer details.

`gomar_stock_alert_summary` counts tracked products by stock alert for the
selected branch without listing products or balances. Negative takes priority;
nonnegative balances at or below the minimum count separately. An inactive
branch stock feature returns no counted products.

`gomar_read_reservation_setup` reports whether a branch has reservation
settings, whether the venue is enabled, whether public reservations are
enabled in Gomar, and the timezone. It does not report contact settings,
policy text, or venue identity. Enabled flags do not confirm the public
page is reachable or that a date, party or table can be booked.
