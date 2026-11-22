# Propose a document review

The feature is off unless Gomar starts with
`-enable-mcp-document-review-proposals`. An authorized reviewer creates a
named proposal session for one outlet and the action `document_draft_review`.
It lasts at most 15 minutes and expires or can be revoked with its login.
It cannot be extended by selecting another action. Existing POS proposal
sessions keep their original action and limits.

The tool proposes reviewing an existing draft using its document ID, current
revision and a mandatory audit reason. The reason is trimmed and must be no
more than 1,024 UTF-8 bytes. Configure a dedicated client with
`--proposal-session-action=document_draft_review`; keep its token in a private
file, not in chat or commands. The bearer can submit a proposal but cannot
see the draft preview or approve it.

The issuing login reviews the exact proposed revision, type, currency, total
and reason in Gomar. On approval, Gomar rechecks the current reviewer role,
outlet grant, draft status and revision. It marks the draft reviewed and
records the reason in the normal audit in one transaction. No amounts, files,
payables or payments are changed. A stale proposal cannot apply.
