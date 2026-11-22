# Web reservation contacts (off by default)

The web form collects a name, a phone with an explicit international
country code, and an optional note when both the server option
`-enable-reservation-contacts` and the venue's `collect_contact` setting
are on. The server option requires `-public-reservations`. An administrator
of the exact outlet can read or set the venue option at
`GET|PUT /api/v1/entities/{entity}/outlets/{outlet}/reservations/contact-settings`
with the normal signed-in session and CSRF checks for writes. A PUT body is
`{"collect_contact":true}` or `{"collect_contact":false}`. No venue is changed
by migration. Older requests keep their original data and have no contact
link; the old booking form still works at venues not collecting contacts.

The number is validated locally using pinned numbering-plan metadata;
no service is contacted. Spaces, dots, hyphens and parentheses are allowed
between digits. Include `+54 9` for an Argentine mobile or `+54` and the
area code for an Argentine landline. The app does not guess a country
or strip national dialing prefixes.

A private customer row is keyed by organization and normalized phone.
The same number in two outlets of an organization links to the same
internal customer, but an identical number at another organization does
not. This is a visit association, not verification of ownership or
permission to contact the number. The guest name and optional short note
belong to that reservation, not a public customer profile. No list,
lookup, match flag, or contact is exposed in guest responses or MCP.
Opting in to contact collection does not send messages; WhatsApp and
feedback are unchanged. Turning collection off stops new collection;
it is not a deletion action. Do not enable it before the operator has
set an appropriate access and retention policy.
