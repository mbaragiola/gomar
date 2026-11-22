# Wiki edit proposal adapter

This change provides Go hooks only. It adds no HTTP endpoint, credential,
proposal store, chat tool or browser UI.

The typed request is `{article_id, expected_version, title, body}`. The
approval workflow supplies the authenticated actor, entity and branch, never
the model. Use `wiki_edit.v1` as the fixed purpose. Titles are trimmed and
limited to 120 characters; the body is left as entered and limited to 12,000
characters and 48 KiB. Blank text and no-op edits are rejected.

- `prepareWikiEditProposal` checks the actor's native wiki editing permission,
  exact branch grant and current article version. It writes nothing. Its
  private preview contains the old and new title/body and the exact version.
  Show this only in the same actor's authenticated browser, as plain text.
- Keep a review digest that binds purpose, scope, article, expected version
  and both sides of the preview. If the outer workflow needs stable retries,
  compare a separate input digest before taking a new snapshot.
- On explicit browser approval, `applyWikiEditProposal` checks the digest,
  active session, current permission and branch, and exact current article
  version and text. It then calls the native versioned wiki update. A stale
  preview returns 409 rather than overwriting a newer edit.
- The outer workflow must check the proposal-only key, CSRF, expiry,
  revocation and replay. Record its terminal receipt in the **same**
  transaction as the wiki version and its immutable actor audit. Acquire
  the write lock on the entity before any shared entity lock. Do not expose
  either side of a stored preview in the chat receipt.

This action never creates or deletes an article, changes access, or calls
a provider. It has no dependency on Turnate.
