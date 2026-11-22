# Propose a wiki edit through MCP

This workflow is off by default. Enable it with
`-enable-mcp-wiki-edit-proposals`. It requires an administrator with native wiki
edit access and a grant for the selected branch. The read key, wiki creation
key and document proposal key cannot submit edits. An edit key cannot read
articles, create one, approve its own proposal or perform any other action.

The model submits the article ID and current version along with the proposed
text. The same signed-in user reviews the exact old and new text in Gomar.
Approval writes one native version and its actor audit. A newer edit makes the
proposal stale rather than overwriting it. Rejecting makes no change.

## API

- Browser session, exact Origin and CSRF: issue with
  `POST /api/v1/auth/mcp-wiki-edit-proposal-token`, body
  `{"entity_id":1,"outlet_id":2}`. The 201 reply contains `id`, `token` and
  `expires_at`. Revoke with `DELETE /api/v1/auth/mcp-wiki-edit-proposal-token/{id}`.
- Submit with that bearer key at
  `POST /api/v1/mcp/entities/{entity}/outlets/{outlet}/wiki-edit-proposals`.
  The body is `{"request_id":"<lowercase UUIDv4>","edit":{"article_id":1,"expected_version":3,"title":"...","body":"..."}}`.
  Reply 201 (or 200 for an identical replay) contains only `id`, `status`
  and `expires_at`. Reusing the same ID with different input returns 409.
- The same-origin browser lists at
  `GET /api/v1/entities/{entity}/outlets/{outlet}/wiki-edit-proposals` and
  opens a proposal at `/{id}`. Pending details add `edit`, consisting of the
  article ID, expected version, and `before` and `after` text. Render both as
  plain text. The browser also receives the approval `digest`.
- To decide, `POST /api/v1/entities/{entity}/outlets/{outlet}/wiki-edit-proposals/{id}/decision`
  with `{"decision":"approve","digest":"<review digest>","confirm":true}` or
  `decision: reject`, using the same-origin browser session and CSRF.
  Stale edits return 409 and may still be rejected. Expired or revoked
  proposals return 410. Only the browser receives the approved
  `article_id` and `article_version`.

The key and pending proposal share a 15-minute deadline. Terminal states clear
the private preview immediately; a regular cleanup clears expired previews
and removes minimal receipts 24 hours after the key expires. Backup retention
is independent. See [the adapter](MCP-WIKI-EDIT-ADAPTER.md) for text limits and
transaction rules. This workflow makes no provider requests.
