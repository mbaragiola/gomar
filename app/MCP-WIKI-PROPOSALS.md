# Review wiki pages proposed through MCP

This optional feature lets a chat client queue a **new** wiki page. It does
not let the client publish, edit, or delete an article. Ordinary MCP read
keys cannot queue proposals. Proposal keys cannot read business data or
approve a proposal.

1. Start Gomar with `-enable-mcp` for ordinary reads and
   `-enable-mcp-wiki-proposals` for wiki proposals. The proposal option is
   off by default.
2. Sign in as an administrator with access to the selected outlet. Under
   **Propuestas para la wiki**, create a separate proposal key.
3. Put the two keys in different private files, readable only by your
   account. Pass the paths to the local `gomar-mcp` process with
   `--token-file` and `--wiki-proposal-token-file`. Do not paste either key
   into a chat or put it on the command line.
4. Call `gomar_propose_wiki_page` with the entity, outlet, a UUIDv4, title,
   and plain-text body. An identical retry uses the same UUID and content.
   A pending receipt means the page has **not** been published.
5. In a signed-in Gomar browser session, open the exact proposal, review its
   text, then use the separate confirmation to publish or reject it. The
   same staff account must still have access. Approval writes the page and
   its first version once, even if requests overlap.

The key and any pending proposals expire no later than 15 minutes after
issue. Revoking the proposal key invalidates its pending proposals. Closing
the panel hides the key but does not revoke it. Gomar removes the text
from the proposal record when it reaches a terminal state; an approved
article remains in the wiki. The chat client never gets the body, review
digest, or approval controls in its receipt.
