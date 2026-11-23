# Gomar agent instructions

## MCP coverage is required

- When you add or change an app endpoint or view, ship the corresponding
  workflow through Gomar's MCP server in the same pull request. Use the same
  validation, permissions, feature flags, and business and outlet scope as
  the app. Listing a tool, returning a summary, or queuing a proposal alone
  does not cover the full workflow.
- In that PR, test the workflow through the packaged MCP server for both
  allowed and denied callers. Cover missing permissions and a different
  business or outlet where applicable. A denied call must not disclose
  protected data or make an unauthorized change.
- If a step must stay native-only, document the exact step and why in the
  PR and in [MCP workflow coverage](app/MCP-COVERAGE.md). If an operator
  needs to act, give them a scoped handoff to Gomar. Where explicit human
  approval is required, MCP may prepare a proposal but only the authorized
  operator can approve it in Gomar. The handoff must not bypass permissions
  or expose private files, contact details, credentials, or other protected
  data to MCP. Test the allowed and denied paths for the handoff.
- Reviewers: do not approve or merge a change without the same-PR MCP
  workflow and permission tests, or a documented native-only exception
  with the required handoff and permission tests.
