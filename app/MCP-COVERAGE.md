# MCP workflow coverage

Coverage means a staff member can complete the workflow from a supported
client, subject to the same permissions as the app. A list or summary is
not full coverage of the underlying workflow. A proposed change is not an
applied change. Features disabled in the app are not available via MCP.

| Workflow | MCP coverage now | Work still needed |
| --- | --- | --- |
| Identity and business scope | Current user, businesses, and outlets | Management views; changes to access stay in Gomar |
| POS | Recorded sales totals, product catalog, and separate create, name, price, and availability proposals for confirmation in Gomar | Register sales stay in the native POS; Gomar does not expose shift management |
| Inventory | Tracked balances, thresholds, movements, and branch-wide stock alert counts; propose receipts, waste, physical counts, initial disabled product setup, and changes to an existing configuration’s minimum or tracking for confirmation in Gomar | Branch-wide settings stay in Gomar; bills of materials are not implemented |
| Wiki | Search and read by ID; propose new pages and versioned edits for confirmation in Gomar | Gomar does not offer deletion |
| Delivery | Recent order status with limited fields; aggregate status counts for the latest 100 inbox records | No native staff write workflow; provider calls and credentials stay outside the read interface |
| Documents and intake | Outlet-scoped list, detail, and document totals by currency; propose draft creation, review, or date and amount edits for confirmation in Gomar | Other draft changes and safe handoff for files; app OCR and intake confirmation parity |
| Suppliers | Active supplier names and IDs in the business | Guarded create and edit |
| Payables and payment records | Outstanding and overdue totals, counts by status, outlet-only payable and payment record details, and one payable’s ledger history | Guarded accounting entries and reviews, not movement of money |
| Reconciliation | Scoped run counts by status, paged run list, metadata by ID, and proposal for an empty draft | Source handling, matches, decisions, and review stay in Gomar |
| Reservations and menus | Daily reservation totals, staff setup state, scoped staff queue and detail, available public reservation times by day and party size, published menu metadata, separate confirmation and cancellation proposals, and preparing a numbered table or one future time while the venue is disabled | Booking, menu and venue settings stay in Gomar |

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
a payment, or rewrite prior sale items or stock movements. The opt-in
new-product proposal creates one catalog item at revision 1 after browser
approval, without a stock or sales change.

An optional reservation confirmation proposal lets the issuing browser
login confirm one existing pending reservation for its branch. It shows
the local start time, party size, and the exact available table. If that
table changes or becomes unavailable, the approval fails rather than
booking another table. There are no guest messages, cancellations, or
changes to venue policy.

With reservation cancellation proposals enabled, the issuing browser login
can approve cancellation of one future pending or confirmed reservation.
The preview shows the existing time, party size, status, and assigned table
if there is one. Approval releases that table. It does not notify the guest
or change venue settings or policy. The browser, not the MCP client,
applies either reservation decision after the issuing login confirms it.

Document totals mirror the selected outlet in Comprobantes. They count
draft and reviewed documents, omit deleted records, and subtract credit
notes. ARS and USD are separate. Each `total_minor` is a signed integer
string in minor units. There is no date filter, and these are not sales,
payables, or payments.

An optional stock minimum proposal changes only the warning threshold for
one actively tracked item at one branch. Zero is a valid threshold. The
issuing browser login reviews the prior and proposed minimum. This does not
change tracking, inventory quantity, product availability, or sales.

`gomar_reservation_availability` asks for an exact branch, Buenos Aires date
(today through 60 days out) and 1–12 people. It returns the same candidate
times as the guest page, only while public reservations and the venue are
enabled. Pending requests do not hold tables. A returned time does not book
a table or guarantee the guest can submit a request.

An optional supplier legal-name proposal changes an existing active
supplier's name across the whole business. The active branch grant allows
the request, but does not limit its effect to that branch. The issuing
browser login reviews the exact old and new names. The proposal cannot
change a tax reference, affect a posted payable snapshot, or add a supplier.

The read-only reconciliation setup view shows whether nonempty source
metadata exists on each side, whether the reference rule is enabled, and
whether known setup limits apply. It does not open files, read source rows,
or verify that native preparation will succeed.

A stock tracking proposal changes only one existing configuration in one
branch. It never changes the minimum, estimated balance or movement history.
Sales recorded while tracking is off, including offline sales synced during
that time, do not deduct stock. Turning tracking back on continues from the
previous estimate; it does not backfill missing deductions. See
[stock tracking proposals](MCP-STOCK-TRACKING-PROPOSALS.md).

First-time product stock setup is a separate opt-in proposal. Approval saves
only an explicit minimum for warnings, with tracking disabled. Turning
tracking on later requires the separate tracking action. See
[product stock setup](MCP-STOCK-PRODUCT-SETUP-PROPOSALS.md).

A table proposal adds exactly one numbered table to an existing disabled
venue. It cannot enable reservations, add shifts, change requests or
contacts, or send notices. Gomar cannot remove the table; see
[disabled venue table proposals](MCP-RESERVATION-TABLE-ADD-PROPOSALS.md).

A slot proposal prepares one future Buenos Aires start for an existing
disabled venue. It cannot enable reservations or change requests or
contacts. Gomar cannot remove the slot; see
[disabled venue slot proposals](MCP-RESERVATION-SLOT-ADD-PROPOSALS.md).
