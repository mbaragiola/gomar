# Proposed POS catalog creation

This adapter registers no tools, routes, keys or settings. It accepts one
trimmed name, positive integer ARS cents within the native price bound, and
an explicit active/inactive boolean. It cannot set stock or change sales.

Preparation requires the current administration role and grant on the
selected active branch. It reads the exact name, amount, availability, and
branch catalog count and limit under the native outlet lock. It saves
nothing and does not reserve space. Approval rechecks the current browser
session, grant, digest and catalog count under the lock. If the number of
products has changed, prepare a new proposal.

The eventual browser handler must additionally lock the pending proposal,
require the exact issuing login and explicit confirmation, and store actor,
branch, action and resulting product ID and revision in the terminal
receipt in the same transaction as the native revision-1 insert.
Approved retries must return that receipt without calling the adapter
again. There is no change to existing revisions, sale history or stock.
