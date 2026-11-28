# MCP workflow coverage

Coverage means a staff member can complete the workflow from a supported
client, subject to the same permissions as the app. A list or summary is
not full coverage of the underlying workflow. A proposed change is not an
applied change. Features disabled in the app are not available via MCP.
For everyday examples, see the [Spanish prompt guide](MCP-GUIA-DE-CONSULTAS.md).

| Workflow | Shipped MCP reads and opt-in proposals | Intentionally stays in Gomar or is not built |
| --- | --- | --- |
| Identity and business scope | Current user, businesses, and outlets; propose a display-name change for one active outlet | Native only: business and outlet administration beyond that rename, membership and grant management, credentials and provider sign-in. |
| POS | Recorded sales totals, own sale time evidence by exact reference, product catalog with exact barcode lookup, one exact saved table check and native handoff; propose product creation and separate name, price, availability or barcode changes | Native only: scan into the local cart, save a tab-local untendered cart, explicitly retire an inspected check with no active tender, preview/print an unsaved table check, register sales, checkout and offline sales sync. Not built: editing or settling a saved table check, and shift management. |
| Inventory | Tracked balances and thresholds, including an exact configured item by ID, movement history, filtered alert pages and stock alert counts; propose receipts, waste, physical counts, initial disabled product setup, and changes to a product’s minimum or tracking | Native only: branch-wide settings. Not built: bills of materials. |
| Wiki | Search and read by ID; propose new pages and versioned edits | Not built: deletion. |
| Delivery | Recent order status with limited fields; status counts for the latest 100 inbox records | No native staff write workflow. Provider calls and credentials are not part of MCP. |
| Documents and intake | Scoped list, detail and totals by currency; propose draft creation, review, date and amount edits, or separate printed-number and printed point-of-sale corrections | Native only: other document edits, deletion and private file handling. Provider OCR is disabled; staged OCR confirmation is not a shipped workflow. |
| Suppliers | Active supplier names and IDs; propose a legal-name correction that applies across the business | Native only: supplier creation, tax identity and superuser catalog status changes. |
| Payables and payment records | Outstanding and overdue totals, status counts, outlet-only payable and payment record details, one payable’s ledger history, and one document’s eligibility for posting | Native only: creating payables, recording and reversing internal payments, and receipt handling. MCP reads do not verify bank activity or move money. |
| Reconciliation | Scoped counts, paged run list, metadata and setup by ID and a read-only close eligibility check; propose one empty single-outlet draft | Native only: private files, source rows, preparing matches, decisions and closing review. Setup metadata does not prove readiness. |
| Reservations and menus | Daily counts, setup flags, one exact configured future start and one numbered table check, staff queue and detail, public candidate times, published menu metadata; propose confirmation or cancellation of an existing request and prepare a table, future slot, or fixed informational cancellation notice for an existing disabled venue | Native only: public booking, guest and contact details, custom venue settings and enablement, actual menu files and publication. Preparation never enables or books anything. |

The packaged live server registers 42 baseline read tools. Discovery does not
guarantee access: app flags and current permissions determine which queries
can run. There are also 26 distinct opt-in proposal actions, not 26
simultaneous writes. Of those, 22 are named actions and four use distinct older
credential options. The standard connection selects at most one named action. As a separate
opt-in, [two local lanes](MCP-LOCAL-PROPOSAL-LANES.md) allow POS rename and
stock minimum proposals together for one pinned outlet. Each keeps its own
credential and browser approval. Other action combinations are not supported.

Start Gomar with `-enable-mcp` before enabling any proposal flag. Add
`-enable-pos` for POS proposal sessions and catalog or product proposals,
and `-enable-stock` for stock proposals. Reservation confirmation and
cancellation also require `-public-reservations`. The preparation
proposals for a table, future slot or informational notice can remain
enabled while public reservations are off.

A separate draft number proposal changes only the printed number and records
one native draft revision and audit entry. It leaves the branch, point of sale,
issuer, other document data, attached files and financial records unchanged.
See [printed document number proposals](MCP-DOCUMENT-NUMBER-PROPOSALS.md).

Tenant legal-name and tax-ID changes, outlet creation, and outlet status
changes stay in Gomar under **Administración → Gestionar accesos**. Approved
MCP outlet renames and native tenant catalog changes write the same internal
audit trail. MCP and browser reads do not return the audit trail.

Creating an outlet requires an ordinary tenant administrator. In Gomar,
the administrator who creates it receives the first outlet grant. In the
platform console, choose an active administrator of the selected entity
for the grant. Both paths record the grant with the outlet creation. This
step assigns access and stays native-only. A read key can discover a
new active outlet after its owner has the grant. It cannot create outlets,
assign grants, or discover an outlet outside its existing scope.

## Managed employee accounts

Employee invitation, temporary password display, first sign-in, password
replacement, resets and deactivation are native-only. They set or reveal
credentials and change a person's organization access; do not ask anyone
to enter a username, password or recovery detail in chat. There is no MCP
tool that lists managed employees, issues credentials or changes their access.

For the handoff, open Gomar without any credentials or employee IDs in the
URL. Sign in as an organization administrator, select the intended
organization, then choose **Administración → Gestionar accesos → Empleados de
la organización**. Use **Más empleados** if the first page does not show
the employee. The app checks current management permission and permitted
locations. A clerk, another organization’s administrator or an ungranted
provider account cannot manage these employees. The employee uses **Ingresar
como empleado** in Gomar and changes the temporary password there.
This preview does not issue MCP credentials to managed employees.

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

Optional POS catalog proposals let a branch manager approve a price,
availability, or exact barcode change. Each action preserves the other fields
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

### Native supplier payments

Draft creation, posting, reversal, and private receipt handling require an
authorized operator in Gomar. The payment workspace clears its open draft,
allocations, and selected receipt on a branch switch. History and invoice
allocations still cover the operator's authorized branches; history shows the
payment's branch, and **Abrir** uses the selected branch. Nothing is deleted or
posted by switching branches. Existing MCP reads remain pinned to one branch
and do not include payment references, methods, or receipts.

Deactivating an outlet preserves its posted payables, payment history,
and outstanding balances. An operator with Treasury permission and
current grants can review the archived ledger and authorized payment
allocations in **Cuentas a pagar**. A payment stays read-only while its own
outlet or an allocation outlet is inactive. A tenant administrator must
reactivate the applicable outlets before resolving the balance, reversing
a payment, or accessing a private receipt. This does not reopen other
outlet workflows while the outlet is inactive.

MCP payable and recorded-payment lists, exact records, and payable
movements accept an entity-wide read key for an explicitly named outlet
with a current grant. Keys pinned to inactive outlets remain unusable.
The existing one-outlet finance projections and cross-branch settlement
exclusions still apply. Payment entry and private files stay in Gomar.

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

An eligible invoice with a valid printed reference may include a relative
`handoff_url` for a single pinned branch. Only a currently authorized
finance operator signed in to Gomar can open it. The page rechecks the same
branch and eligibility and shows only the document ID and printed reference,
so the operator can find it through **Comprobantes → Buscar por referencia
impresa**. It never opens the document or Treasury automatically. Changes
in scope or eligibility make the handoff unavailable; sign in normally and
reopen if needed. The link is not approval, posting, or a payment.

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
Once Gomar confirms a native close, its reconciliation workspace refreshes
the run's history and shows the saved closed status and unresolved count in
the run list. This refresh uses the existing native read permissions; it
does not extend the MCP handoff or expose review data to MCP.

### Exact POS catalog product

`gomar_read_pos_product` reads one known product ID in the selected
business and branch, when POS is enabled and the caller has current
business-read permission for that branch. It returns only the product ID,
name, price in ARS cents, current catalog revision, active status, and the
exact barcode when assigned.
Inactive products remain readable, like in the native catalog. It does
not search, show stock or costs, record a sale, or change the product.

Use `gomar_pos_catalog` with `barcode` for an exact branch match. Pass
the code as text, including leading zeros. A catalog manager can open
**Gestionar códigos de barras** or propose one assignment or removal
with `gomar_propose_pos_barcode`. The proposal takes effect only after
browser approval; it never changes stock or sales.

Scanning and the cart stay in the register browser tab. Open **Punto de
venta**, select the business and branch, enable the register in that tab,
then scan or type a code and press Enter. An offline register uses its
saved catalog while its grant is valid. Enable it again online to get new
codes. Before moving a code to a different product, refresh every
register: an offline register can still match the old product until its
grant expires or the register is re-enabled. A scan adds to the cart; it never checks out or charges a sale.

### Paper table check (native only)

Open **Caja** (`/pos`) in Gomar, sign in as staff allowed to use that
branch's POS, and enable the register in that tab. Add the items, choose
**Ver cuenta de mesa**, then **Imprimir cuenta de mesa**. The supported
handoff is the existing POS workspace; there is no link or token that
opens a cart or restores a draft. A staff user without the selling role
or branch grant cannot enable that branch's register.

Only that browser tab has the unrecorded cart and can open the device's
print dialog. A preview doesn't register a sale, record payment, reserve
a table, contact a fiscal device, or generate a fiscal invoice. The
printed check is not an invoice or proof of payment. No endpoint or
MCP tool reads, writes, or prints the cart; catalog reads do not gain
access to it. The check relies on the current local register grant.
The operator must use their separate fiscal issuance process where required.
To check the allowed and denied handoff paths and Chrome's printed layout,
run `bash scripts/qa-mcp-stock-journey.sh --with-pos-prebill` with
`pdftotext` installed. The test saves no sales and never talks to a
physical printer.

### Save and reopen an untendered table check

With both POS and its opt-in `-enable-pos-table-checks` flag enabled,
Caja can transfer its current browser cart to one exact numbered table at
the authorized branch while online. Only Caja can start the transfer, block
checkout for the local cart, or retry the exact request. A pending request
survives a reload, sign-out, lock, or shift expiry on that browser. It can
only be retried from an authorized register for the same actor, entity and
branch, including after opening a new shift. No MCP caller has this
unrecorded cart or can create one from guessed items. Confirmation clears
the transferred local cart, even if the check was later retired or
finalized. A verified refusal makes that exact request permanently unable
to create a check; Caja can restore a cart that still matches the active
register's catalog. If the cart cannot be restored, Caja preserves a
read-only list of the original products and quantities for that cashier and
branch until they mark it as reviewed. It is not a sellable cart and opening
or acknowledging it does not save anything on the server. If the outcome
is still unclear, Caja keeps checkout blocked. A missing table on lookup is
not proof that a save failed.
Saved rows use the exact current catalog revisions and can be consulted by authorized
staff in the same branch. The transfer doesn't register a sale, payment,
stock change, reservation, or fiscal document. An operator can retire a saved check and free the table for another check; this doesn't edit or settle it. Retirement preserves the original check and reason.

`gomar_read_pos_table_check` returns the saved table number, total in ARS
cents, fixed untendered status, and a same-origin **Caja** handoff. It uses
a key pinned to the exact branch and the same POS roles as the native view.
It never returns item details, payment data or a write credential.
The link only prefills the table when a current authorized register matches
the exact business and branch. The operator then chooses **Consultar mesa**;
the browser API rechecks current scope before showing the saved items.
Opening the link never requests or saves a cart, and changing the link does
not change the authorized branch.
Retiring is native only. The handoff lets the operator inspect the current
items inside Caja before confirming a reason. MCP cannot confirm that a
check was saved in error or cancelled without consumption or payment.
A stale browser action targets the originally inspected check, not a
replacement at the same table. After retirement the MCP exact lookup
finds no active check until a replacement is saved; it does not reveal
retirement history or create a replacement.

Native sale and table-check writes, including exact retries and verified
refusals, recheck current
membership, role and the selected branch grant in their write transaction.
MCP still only reads or hands off these operations. Catalog and stock
proposals still require the existing scoped browser approval.

After deploying this change, reload every already-open Caja register online
before using saved table checks. Older cached register code does not clear
or hold the local cart; the server still serves those clients during the
transition. For a cart already in use on an older register, compare it with
the saved table before recording a sale or re-entering it. An authorized
operator can inspect the scoped handoff in Caja, but it does not release a
pending cart without its exact save result.

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

### Revoke your own read connections in Gomar

Credential management is native-only. In **Conectar Claude o Codex**, a
signed-in operator returning to the same active, granted branch can see
only their own still-valid read access IDs and issue/expiry times. A
previously issued secret cannot be recovered. The list never includes
business-wide keys, proposal keys, other operators, or bearer material.
Each **Revocar acceso** button uses the existing owner-only browser action;
opening the panel does not revoke anything. MCP can use an authorized read
key but cannot list, recover, or revoke credentials.

### Manual recorder for payments handled elsewhere

When the default-off POS manual tender preview is enabled, an authorized
clerk can open a saved active table in **Caja** and use **Registro de medios
de pago** to note an amount already handled outside Gomar or correct an
earlier note. A correction leaves the original in the history. Neither
action charges, refunds, posts a sale, releases the table, adjusts stock,
or issues a fiscal document. Active payment notes block retirement even when
the preview is turned off.
If every note was corrected, a clerk can retire the check in Caja. The
original notes and corrections remain in the journal.

`gomar_read_pos_table_check` reports `manual_records` when a saved check
has tender history unless finalization is enabled and the check is ready for review. This is not evidence of a paid balance. It gives the
same branch-pinned native link as an untendered check; open it, sign in,
enable that branch's register, and consult the table. If the preview is
disabled, status still warns about the existing history. Only the scoped
native register can accept a clerk's manual note or correction and show
amounts, reasons, or individual entries. MCP cannot enter funds-handling
instructions or inspect the journal; the link carries no credentials or
tender identifiers. Role and branch restrictions apply on both sides.
Managed employee sessions do not yet have the saved-table or tender write
actions and do not see those controls.

### Finalize a covered saved table check

With the opt-in `-enable-pos-table-finalization` flag (requires POS, saved
table checks and manual tender), `gomar_read_pos_table_check` reports
`ready_for_review` when all of the saved check's amount is covered by active
manual records. This is not proof of payment. The pinned business and branch
and selling role still apply, and no payment details are returned.

Open **Caja** from the scoped handoff, consult the table, and review the
payment breakdown. Only the authorized clerk in Caja can confirm the exact
check and version they reviewed. This action stays native-only because the
clerk must verify payment handled outside Gomar and confirm the exact
breakdown. Opening the link never records a sale. A confirmation records
one POS sale, counts it once in sales summaries, and releases the table.
The normal optional stock rules apply. No provider charge, refund, fiscal
document or fiscal cancellation is made. The ordinary single-method
offline sales contract does not change.

### Prices for confirmed sales and saved table checks

When Caja sends a confirmed sale, Gomar prices it from the exact product
revision supplied for that outlet. The server uses the stored unit price,
including for a prior revision that was active. A later price change or
product deactivation leaves that revision available for queued sales. A
missing revision, a revision marked inactive, or a revision from another
outlet rejects the sale without recording it. Caja keeps the rejected sale
for review. Exact retries return the original receipt.

Price revisions have no age limit. The server receives the same sale
request online or after a delay and has no trusted capture time. The local
register lease limits new offline sales, and the optional reported clock
can flag a delay for review. Neither expires a queued sale or changes
its registered price.

Saving a new table check prepares an open order. Every item must match the
current active revision, so a price change or deactivation requires the
operator to review the current catalog before saving. A check already saved
keeps its original items and prices. An exact retry, and later finalization
when enabled, use that saved amount.

### Time evidence for directly queued Caja sales

Caja records the browser-reported UTC clock once when a new sale is queued
under a lease that has learned `hora_informada_caja_utc_v1` from the server.
On reconnection it only sends `hora_informada_caja_utc` if the fresh catalog
advertises support. Older queue entries and registrations never gain a
reported timestamp from their prior local storage time or from a replay. A
replay still matches actor, outlet, items, revisions, quantities and recorded
payment method. It returns the first registration and evidence (or lack of
evidence), even if the retry says something different about the clock.

Caja and `gomar_read_pos_sale_time` show the original Gomar registration
time and the optional, unverified clock evidence separately. The read uses
the exact sale request reference and the current Caja role, entity, outlet
grant and actor. MCP also requires a token pinned to the outlet. Employee
browser sessions can use their existing native Caja access but cannot issue
MCP tokens. Unauthorized reads provide no timing or handoff. The handoff
opens an exact lookup that requires the same signed-in operator to explicitly
request it; it does not arm a shift, replay or create a sale. Malformed
timing links stay on a read-only error page, not the register.

A claim more than five minutes after registration or more than 24 hours
before it is marked for review, not rejected. A legitimate backlog can need
review too. The reported time never proves when a sale or payment occurred.
Monthly sales totals still use Gomar's registration time in Buenos Aires;
stock still records once on first registration. Sale capture and offline
sync stay native-only because they use the operator's browser-local cart,
clock and trusted, scoped lease. No payment, fiscal issuance, historical
correction or backdating is provided.

### Caja pre-close review (native only)

In **Caja** (`/pos`), use the existing authorized register for the selected
outlet and choose **Antes del cierre → Consultar ventas de este navegador**.
This reads only the existing IndexedDB sales belonging to the account and
outlet of the active lease in this browser. It neither queries the server nor
arms a shift, sends the queue, or changes a sale. If the browser's store is
missing or unavailable, the register reports a read error without creating it.
Data outside this browser profile, account or outlet is not verified. Do not
treat it as clearance to count stock or close the month. Check and sync
each register first.

The comparison uses the original Gomar registration and acknowledged,
unverified till time for synchronized sales held in this browser. It includes
registrations in the current and previous Buenos Aires month (relative to the
browser clock) and accounts separately for missing times. A legacy row is
never dated from its enqueue timestamp. Pending and review states are local
observations, not proof that Gomar never received the sale.

Caja queries pending sales for sync and shows all unresolved sales with
up to 50 acknowledged sales at a time. Use **Ventas sincronizadas anteriores**
to find older references. The register keeps acknowledged receipts locally
because the server timing lookup needs the exact reference and pre-close
uses the receipts. Local deletion would require a scoped server lookup
that can find older references and preserve the evidence used at pre-close.
Caja retains pending and attention sales for review.

For a sale marked **Necesita revisión**, choose **Verificar registro**
in the same Caja. Gomar looks up the original reference under the current
cashier and outlet. Caja compares the registered amount, payment method,
and product revisions and quantities with the saved sale. An exact match
acknowledges the local row. A difference or unavailable result keeps it
in review with the reference to bring to administration. The lookup never
reposts a sale. The comparison needs the device's local receipt, so it stays
in Caja. MCP retains the scoped timing handoff for known references.

The register queue and the cross-month list inferred from browser-held
receipts are native-only: MCP has no access to this device's IndexedDB or
lease. To verify server-held evidence for a **known exact reference**, use
`gomar_read_pos_sale_time` and its scoped **Horarios de la venta de Caja**
handoff above. The server and handoff still require the original cashier,
business and outlet; they deny unauthorized readers without leaking timing
or receipts. No MCP listing or cross-register permission is added.

### Human signup consent: local HTTPS fixture only

The prospective owner opens **Revisá el alta** in the isolated loopback
fixture using the exact ordinary provider account and original session.
Gomar resolves the single eligible plan from PostgreSQL for that account
and the configured audience. They can review the intended
organization and branches, explicitly confirm in Gomar, and reopen
the confirmed or dormant status only from their original active session. This is
native-only: a human must see and accept the exact display, not send a
bearer credential, an email, a private business identifier or a link with
an asserted scope. An assigned prospect has no operational membership
or MCP key from this workflow. An existing unrelated operational MCP key
cannot discover, review or approve signup, read its pre-membership
status, or receive private signup references. The CSRF-checked browser
read is a personal native operation, not an MCP handoff or a credential. It keeps only its prior authorized scope. The exact approved
display also stays in native consent and the protected recovery draft;
the digest, legal name, CUIT, and private branch-label mapping are not
MCP data. The protected operator fixture is a native recovery screen
for a separately approved case, not an MCP action or handoff. Case
references, staged labels, legal data and tickets never go through MCP.
The draft and dormant install remain unregistered native operations.

The default app has no signup page, API or MCP handoff. The only opt-in
route here is an authenticated localhost HTTPS test fixture with a
recovery-authored local plan, not a sharable link or a hosted signup rollout.
The exact synthetic HTTP/browser tests cover the intended owner and
changed session and a persisted exact recovery draft after a fixture
restart. The status tests verify both recorded stages across restart,
with no reissue, mismatch disclosures or side effects. The packaged
MCP check covers an independently scoped operational key plus signup,
status and plan-authoring denial and an unassigned owner. The operator
fixture verifies exact recovery scope, one explicit save and ordinary
session, stale, changed and replay denials; the owner browser sees only
the exact authored display.

### Hosted discovery read fixture

The two existing lists, `gomar_list_entities` and `gomar_list_outlets`,
share the native entity/outlet admission check in the isolated
[hosted discovery fixture](HOSTED-DISCOVERY.md). The proof keeps one
ordinary account and its already issued exact credentials across two
paid scopes in the same disposable database. Suspension and expiration
hide the affected entity and deny both of its discovery calls without
disclosing private paid references. The other scope and self-hosted
control keep their prior grants. There is no new MCP tool or broader
hosted authorization.

The same fixture also gates `gomar_read_pos_sale_time` and the existing
native exact handoff in their shared timing SELECT. The original cashier,
current selling role and exact outlet credential remain required. Real
Chrome and the packaged tool replay the same reference after suspension
and expiration; both deny without returning clocks or a handoff. A
same-entity unmapped outlet is denied even with its own existing grant.
The unaffected paid and self-hosted scopes still return their own exact
receipts. No new tool, device queue access, sale write or monthly report
behavior is added.

The unregistered synthetic observation writer has no native operator action
or MCP tool. Only the existing discovery and exact timing reads consume its
fixture evidence; a tool cannot install, renew, revoke, or import an
observation. The actual same-database desktop/mobile and packaged MCP
regression updates fixture evidence through the same internal apply seam,
including admission, suspension, an out-of-order positive, and expiry.

Only the existing `gomar_propose_pos_barcode` and its native review and
approval share the isolated test fixture's barcode-write gate. An exact
barcode proposal key still cannot approve. The same original authorized
browser shows and confirms the existing change; a stale human view or
already issued key cannot bypass suspension or expiry for that branch.
Tests cover exact scope, allowed native set and human-approved clear,
expired and suspended generic denial with no receipt, and unchanged
revision history, sale and stock on denial. Other proposal lanes and
barcode lookup remain outside this proof.


The existing `gomar_sales_summary` uses the same isolated hosted read
scope inside its receipt aggregate. Existing root and outlet-pinned keys
were tested through the packaged tool before and after suspension and
expiry. Root includes only admitted outlets, an eligible empty outlet
returns zero, and denial exposes no names, amounts or binding data.
The Buenos Aires registration month, original offline replay, stock,
self-hosted empty root, and default behavior are unchanged. There is no
corresponding browser endpoint or new tool.

The isolated hosted fixture covers the existing protected stock panel
and product movement GET alongside `gomar_list_stock` (including alert
filters and cursors), `gomar_read_stock_product`,
`gomar_stock_alert_summary`, `gomar_stock_movements`, and
`gomar_list_stock_movements`. Each read checks the original role and
outlet grant (plus MCP credential scope) and current #044 exact outlet
before any stock data. The browser stock and admission queries share a
read-only repeatable-read transaction, as do the packaged reads.
Not-found has no counts, product labels, movements or paid references.
Allowed empty/disabled stock, stock and sale history, self-host and
default behavior are unchanged. The local browser and packaged
permission checks reuse already-issued access across suspension and
expiry; no tool, write or production admission was added.

For the existing staff reservation queue, the isolated fixture covers
the native queue GET and `gomar_list_reservation_requests`,
`gomar_read_reservation_request`, and `gomar_reservation_status`.
The existing administrator and exact outlet checks (including MCP
pinning) come before the same-snapshot #044 admission check, then the
queue, cursor, reference, or aggregate query. An unadmitted outlet
returns a generic denial; an admitted empty day still returns zero.
The native screen may show the same guest name, table label and
historical policy as before. MCP still omits them, and both omit the
separate private contact records. No public availability, settings,
menu, confirmation/cancellation, proposal, or new tool is involved.

The isolated hosted delivery proof covers the existing native inbox
and the packaged `gomar_list_recent_delivery` and
`gomar_delivery_status_summary` only. The original flag, read role,
outlet grant and MCP pin still apply. Same-snapshot exact #044 admission
returns the generic denial without order identifiers or counts when
scope is lost. Native latest 100, MCP latest 30 with truncation, and
the MCP latest-100 summary keep their existing order and outputs.
External order references and item details stay native-only; MCP
returns no customer contact. No provider, ingestion, polling or
operation changing orders is added.
