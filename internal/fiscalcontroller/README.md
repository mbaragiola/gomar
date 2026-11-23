# Fiscal controller simulator

This package is a local model of explicit fiscal issuance. Gomar does not
call it from the POS, browser, MCP server, payment handler, or any live
printer. It makes no network calls. Simulator output is not a tax document.

Keep three actions distinct:

1. **Table check:** the waiter prints the open check before payment. This is
   a non-fiscal document. Use a normal printer and label the output
   "Cuenta de mesa — no válido como factura". Never submit this to a
   fiscal controller as an issue job.
2. **Payment:** staff record how the customer paid. Tendering does not
   queue a document; split tenders must not produce duplicate invoices.
3. **Fiscal issue:** an authorized worker explicitly issues the document
   under the correct taxpayer and point of sale. This separation does not
   waive the business's obligation to issue a fiscal document when one is
   required. Do not silently treat payment or a printed table check as
   fulfillment. The future POS must make required but unissued documents
   visible and enforce the applicable workflow.

For an ARCA-authorized electronic invoice, an ordinary printer can print
the already-authorized document with the required QR. The print job does
not ask the printer to issue a second document. A fiscal controller is a
separate route that creates the fiscal proof itself.

## State and failure behavior

A single controller instance owns one simulated device. It serializes
device calls, records the immutable issue request before sending, and
returns the original document when a caller repeats a completed sale ID.
The caller must use a stable sale ID; this package does not create one,
choose a fiscal channel, or prevent the same sale from being submitted to
two different devices.

A lost response can happen before issue or after issue. Either result
blocks another send to that device. Read-only reconciliation must find
the matching document, or prove that the exact request was not issued.
A timeout, missing paper, inconclusive lookup, or mismatched document
does not release the block. Issue and lookup evidence must match sale ID,
document kind, point of sale, and exact total. An adapter must read these
from the device or prove the match independently, never echo them from
the request. A conclusive absence permits a subsequent,
explicit retry of the same request; reconciliation never issues.

This simulator can look up a sale ID directly; a particular printer may
not support that. If an adapter cannot prove the exact outcome, it must
remain blocked for operator reconciliation. The in-memory state here
cannot survive crashes and is not safe for production. The request is a
test envelope, not the data needed to issue real fiscal documents.

## Hardware pilot prerequisites

Start with a Hasar SMH/PT-250F 02.00, subject to matching the actual unit.
[ARCA RG 4994](https://biblioteca.arca.gob.ar/search/query/norma.aspx?p=t:RAG%7Cn:4994%7Co:3%7Ca:2021%7Cf:14/05/2021)
lists the homologated documents; this package's capabilities are test
fixtures, not an authorization list. Verify the model, firmware, device
identity, workstation OS, connection (Ethernet, USB, or serial), issuer,
point of sale, and intended document types before testing. The
[manufacturer](https://compania.grupohasar.com/producto/impresora-fiscal-smh-pt-250-f/)
publishes the compatibility note and tools. Confirm status, restart and
read-only reconciliation against lab hardware before committing to a
protocol or platform. Do not enter live credentials or issue real fiscal
documents to test the adapter. For activation and reporting, follow
[ARCA's setup guide](https://serviciosweb.arca.gob.ar/genericos/guiasPasoPaso/VerGuia.aspx?id=281)
and
[current rules](https://biblioteca.arca.gob.ar/search/query/norma.aspx?p=t:RAG%7Cn:5801%7Co:9%7Ca:2025%7Cf:15/12/2025)
with the taxpayer and their authorized technician.

A future production path needs a durable, device-scoped journal and a
single writer across restarts, an authenticated local bridge with outbound
connectivity and least privilege, and a sale-level guard that precludes
issuing the same sale through both ARCA electronic and controller routes.
Do not expose an unauthenticated printer to the Internet or make a
browser's access to local hardware a requirement. Pair the bridge to the
correct business and location. Future app endpoints or views must include
MCP parity or the tested native handoff required in AGENTS.md. This slice
adds no such surface.

## Local check

`go test -race ./internal/fiscalcontroller`
