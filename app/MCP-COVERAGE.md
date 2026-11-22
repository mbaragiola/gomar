# MCP workflow coverage

Coverage means a staff member can complete the workflow from a supported
client, subject to the same permissions as the app. A list or summary is
not full coverage of the underlying workflow. A proposed change is not an
applied change. Features disabled in the app are not available via MCP.

| Workflow | MCP coverage now | Work still needed |
| --- | --- | --- |
| Identity and business scope | Current user, businesses, and outlets | Management views; changes to access stay in Gomar |
| POS | Recorded sales totals, product catalog, and name-only proposals for confirmation in Gomar | Remaining catalog changes and register operations; Gomar does not expose shift management |
| Inventory | Tracked balances, thresholds, movement totals and bounded movement detail; propose a positive receipt for confirmation in Gomar | Catalog and other adjustments; bills of materials are not implemented |
| Wiki | Search and read by ID; propose new pages and versioned edits for confirmation in Gomar | Gomar does not offer deletion |
| Delivery | Recent order status with limited fields | No native staff write workflow; provider calls and credentials stay outside the read interface |
| Documents and intake | Outlet-scoped list and detail; propose creating or reviewing a draft for confirmation in Gomar | Guarded draft edits and safe handoff for files; app OCR and intake confirmation parity |
| Suppliers | Active supplier names and IDs in the business | Guarded create and edit |
| Payables and payment records | Outstanding and overdue totals, counts by status, and outlet-only payable and payment record details | Guarded accounting entries and reviews, not movement of money |
| Reconciliation | Scoped run counts by status, paged run list, and metadata by ID | Workflow for source handling, matches, decisions, and review |
| Reservations and menus | Daily reservation totals by status and published menu metadata | Guarded staff booking and menu operations |

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
