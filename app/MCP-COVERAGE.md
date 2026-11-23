# MCP workflow coverage

Coverage means a staff member can complete the workflow from a supported
client, subject to the same permissions as the app. A list or summary is
not full coverage of the underlying workflow. A proposed change is not an
applied change. Features disabled in the app are not available via MCP.
For everyday examples, see the [Spanish prompt guide](MCP-GUIA-DE-CONSULTAS.md).

| Workflow | Shipped MCP reads and opt-in proposals | Intentionally stays in Gomar or is not built |
| --- | --- | --- |
| Identity and business scope | Current user, businesses, and outlets; propose a display-name change for one active outlet | Native only: business and outlet administration beyond that rename, membership and grant management, credentials and provider sign-in. |
| POS | Recorded sales totals, product catalog; propose product creation and separate name, price or availability changes | Native only: register sales, checkout and offline sales sync. Not built: shift management. |
| Inventory | Tracked balances and thresholds, including an exact configured item by ID, movement history, filtered alert pages and stock alert counts; propose receipts, waste, physical counts, initial disabled product setup, and changes to a product’s minimum or tracking | Native only: branch-wide settings. Not built: bills of materials. |
| Wiki | Search and read by ID; propose new pages and versioned edits | Not built: deletion. |
| Delivery | Recent order status with limited fields; status counts for the latest 100 inbox records | No native staff write workflow. Provider calls and credentials are not part of MCP. |
| Documents and intake | Scoped list, detail and totals by currency; propose draft creation, review, date and amount edits, or separate printed-number and printed point-of-sale corrections | Native only: other document edits, deletion and private file handling. Provider OCR is disabled; staged OCR confirmation is not a shipped workflow. |
| Suppliers | Active supplier names and IDs; propose a legal-name correction that applies across the business | Native only: supplier creation, tax identity and superuser catalog status changes. |
| Payables and payment records | Outstanding and overdue totals, status counts, outlet-only payable and payment record details, one payable’s ledger history, and one document’s eligibility for posting | Native only: creating payables, recording and reversing internal payments, and receipt handling. MCP reads do not verify bank activity or move money. |
| Reconciliation | Scoped counts, paged run list, metadata and setup by ID and a read-only close eligibility check; propose one empty single-outlet draft | Native only: private files, source rows, preparing matches, decisions and closing review. Setup metadata does not prove readiness. |
| Reservations and menus | Daily counts, setup flags, one exact configured future start and one numbered table check, staff queue and detail, public candidate times, published menu metadata; propose confirmation or cancellation of an existing request and prepare a table, future slot, or fixed informational cancellation notice for an existing disabled venue | Native only: public booking, guest and contact details, custom venue settings and enablement, actual menu files and publication. Preparation never enables or books anything. |

The packaged live server registers 40 baseline read tools. Discovery does not
guarantee access: app flags and current permissions determine which queries
can run. There are also 25 distinct opt-in proposal actions, not 25
simultaneous writes. Of those, 21 are named actions and four use distinct older
credential options. The standard connection selects at most one named action. As a separate
opt-in, [two local lanes](MCP-LOCAL-PROPOSAL-LANES.md) allow POS rename and
stock minimum proposals together for one pinned outlet. Each keeps its own
credential and browser approval. Other action combinations are not supported.

A separate draft number proposal changes only the printed number and records
one native draft revision and audit entry. It leaves the branch, point of sale,
issuer, other document data, attached files and financial records unchanged.
See [printed document number proposals](MCP-DOCUMENT-NUMBER-PROPOSALS.md).

## Verification levels

1. App and database tests cover scope, roles, flags, and relevant lifecycle
   rules. Use a fresh local PostgreSQL database per test.
2. Call each tool through the packaged MCP server. Include missing grants,
   a different entity and outlet, and a denied feature where applicable.
3. Record the client verification level that actually ran. Connecting or
   listing tools only proves discovery. A direct tool call through an installed
   client verifies its MCP transport and tool result; it does not invoke a
   model. A scripted or mock-model scenario can test tool selection and how the
   client presents the result, but cannot establish live inference. A live
   natural-language run with a real model is a separate check. Use synthetic
   data and do not record credentials.

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

An optional outlet display label proposal changes only a currently active
branch's name. Access, IDs and connected settings do not change. Some
reports join the current name and can display a prior record under the new
label. See [outlet display label proposals](MCP-OUTLET-DISPLAY-LABEL-PROPOSALS.md).

A cancellation notice proposal prepares fixed informational wording for one
existing disabled venue. It applies only to future requests after staff
separately enable the venue in Gomar. Historical requests keep the wording
the server captured at submission. See [cancellation notice proposals](MCP-RESERVATION-POLICY-NOTICE-PROPOSALS.md).

The separate printed point-of-sale proposal changes only the numeric prefix
of one current unposted draft, leaving the number, business, outlet and
other data unchanged. It does not expand previously issued number-only
keys. See [point-of-sale proposals](MCP-DOCUMENT-POINT-OF-SALE-PROPOSALS.md).

### Payable eligibility

`gomar_check_payable_eligibility` checks one `document_id` for the selected
entity and branch. It uses the same document-state checks and order as
native posting and requires the current supplier-payment permission. A
successful check returns `eligible: true`. A blocked check returns
`eligible: false` with one stable reason: `already_posted`, `not_reviewed`,
`not_invoice`, `nonpositive_total`, `total_out_of_range`, or
`supplier_mismatch`. It returns no supplier, tax identifier, amount, or
receipt data. The read is not a lock, reservation, approval, posting, or
payment. The native request still needs a reason and fresh checks before
any account payable can be created.

### Reconciliation close eligibility

`gomar_check_reconciliation_close_eligibility` checks one `run_id` at the
selected entity and branch with the current reconciliation permission and
access to all participating branches. It returns `eligible: true` or
`eligible: false` with one reason: `not_in_review` or
`unsupported_review_data`. Pending unresolved entries are allowed.
The read does not return counts, rows, balances, account data, or files.
It does not close or reserve the run and does not check a future revision
or operation key.

A run in review with exactly one participating branch may return a relative
`handoff_url`. Only the operator signed in to the same Gomar instance can
open it. It checks current permissions and shows a minimal confirmation of
the run and its current close eligibility, never reconciliation sources,
bank rows, accounts, amounts, candidates, or review details. Multi-branch
and non-review runs never send a link. A signed-out operator can sign in
normally and reopen the link. The handoff does not open regular review
or change data; the operator must navigate to Financial Reconciliation
and use the existing native review and close workflow with fresh checks.

### Exact POS catalog product

`gomar_read_pos_product` reads one known product ID in the selected
business and branch, when POS is enabled and the caller has current
business-read permission for that branch. It returns only the product ID,
name, price in ARS cents, current catalog revision, and active status.
Inactive products remain readable, like in the native catalog. It does
not search, show stock or costs, record a sale, or change the product.

### Find a document by its printed reference

In Comprobantes, use **Buscar por referencia impresa** for the selected
branch. In MCP, use the same four fields as an optional `printed_reference`
in `gomar_list_documents`: fiscal document type, uppercase letter (empty
for no letter), printed point of sale (1–99,999), and printed number
(1–99,999,999). This is the number on a document, not the branch ID. All
four fields are required for a lookup; omitting the lookup preserves the
ordinary list. Results use the current branch grant and business reading
permission. Different issuers can print the same reference. If several
documents match, open the right one explicitly in Gomar rather than using
the first. Opening a match in Gomar rechecks the same branch and reference
in case the document moved or was corrected. With `next_id`, request the
next page using the same reference and branch. Pages are not a snapshot. The filter does not disclose new
supplier details, retrieve files, change a record, or approve a review.

### List drafts for review

In Comprobantes, **Ver borradores** shows only current, non-deleted drafts in
the selected branch. The existing `gomar_list_documents` tool accepts
`status: "draft"` for the same scope; repeat that filter with `next_id` for
another page. The ordinary list is unchanged. This filter and a printed
reference lookup are separate modes and cannot be combined. This list is not
a claim about other pending work or a snapshot across pages.

Opening a draft in the browser rechecks that it is still a draft in the same
branch. Finding one does not open it automatically, mark it reviewed or
create a payable. Review still requires native permission and an explicit
action in Gomar (or the separately issued review proposal and browser
approval). The MCP list does not add any private metadata.
