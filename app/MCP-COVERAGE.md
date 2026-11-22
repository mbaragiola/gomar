# MCP workflow coverage

Coverage means a staff member can complete the workflow from a supported
client, subject to the same permissions as the app. A list or summary is
not full coverage of the underlying workflow. A proposed change is not an
applied change. Features disabled in the app are not available via MCP.

| Workflow | MCP coverage now | Work still needed |
| --- | --- | --- |
| Identity and business scope | Current user, businesses, and outlets | Management views; changes to access stay in Gomar |
| POS | Recorded sales totals, product catalog, and separate name, price, and availability proposals for confirmation in Gomar | New catalog items and register operations; Gomar does not expose shift management |
| Inventory | Tracked balances, thresholds and movements; propose receipts, waste, and physical counts for confirmation in Gomar | Catalog and tracking settings; bills of materials are not implemented |
| Wiki | Search and read by ID; propose new pages and versioned edits for confirmation in Gomar | Gomar does not offer deletion |
| Delivery | Recent order status with limited fields | No native staff write workflow; provider calls and credentials stay outside the read interface |
| Documents and intake | Outlet-scoped list and detail; propose draft creation, review, or date and amount edits for confirmation in Gomar | Other draft changes and safe handoff for files; app OCR and intake confirmation parity |
| Suppliers | Active supplier names and IDs in the business | Guarded create and edit |
| Payables and payment records | Outstanding and overdue totals, counts by status, and outlet-only payable and payment record details | Guarded accounting entries and reviews, not movement of money |
| Reconciliation | Scoped run counts by status, paged run list, and metadata by ID | Workflow for source handling, matches, decisions, and review |
| Reservations and menus | Daily reservation totals, scoped staff queue and detail, and published menu metadata | Guarded staff booking and menu operations |

## Verification levels

1. App and database tests cover scope, roles, flags, and relevant lifecycle
   rules. Use a fresh local PostgreSQL database per test.
2. Call each tool through the packaged MCP server. Include missing grants,
   a different entity and outlet, and a denied feature where applicable.
3. Record real client outcomes separately. A connection or tool list is
   discovery. A direct tool invocation and a natural-language model run are
   different checks. Use synthetic data and avoid recording any credentials.

Use an outlet-scoped key. A denied call is not an empty report. Keep
files, customer contact details, secrets, and provider credentials out of
the standard chat output. Use Gomar when the operator needs to view them.

An opt-in, single-branch reconciliation draft proposal is separate from
reconciliation reads. An issuing browser login can approve an empty native
draft, including its native creation audit. The proposal fixes one active
branch and an explicit match-by-reference setting. It cannot add statement
files or rows, match transactions, close a run, or record a payment.

An optional document draft edit proposes dates and amounts only. It shows
the old and new values, currency, and reason to the issuing browser login.
Approval applies one native draft revision and audit entry. It cannot
change supplier, branch, currency, files, review status, payables, or payments.

Optional POS catalog proposals let a branch manager approve either a price
change or an availability change. Each action preserves the other fields
and adds a native catalog revision. A proposal does not make a sale, collect
a payment, or rewrite prior sale items or stock movements.

An optional reservation confirmation proposal lets the issuing browser
login confirm one existing pending reservation for its branch. It shows
the local start time, party size, and the exact available table. If that
table changes or becomes unavailable, the approval fails rather than
booking another table. There are no guest messages, cancellations, or
changes to venue policy.

The internal reservation cancellation adapter can preview only an existing
future request for an authorized branch. It binds prior status and table
allocation and uses native cancellation to release the table. It does not
send messages or disclose guest or venue-policy data. There is no MCP
cancellation tool or approval route in this adapter.

With reservation cancellation proposals enabled, the issuing browser login
can approve cancellation of one future pending or confirmed reservation.
The preview shows the existing time, party size, status, and assigned table
if there is one. Approval releases that table. It does not notify the guest
or change venue settings or policy.
