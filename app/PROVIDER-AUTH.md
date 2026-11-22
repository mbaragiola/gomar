# Provider sign-in foundation

This change is **off by default**. The executable reads no provider
settings unless `-enable-provider-auth` is explicitly selected. Existing
password login and administrator access are unchanged. Migration
`012_provider_auth.sql` is staged but not in the migrator until the public
sequence contains 011. The flag refuses startup on this branch without
opening provider secret files. Tests apply the SQL to disposable PostgreSQL.
Do not turn on provider login before registration and browser verification.
Do not remove normal password login before the separate admin-session gate.

## Flow and identity

- `GET /api/v1/auth/providers` returns
  `{"providers":[{"id":"google","label":"Google"}],"local_password_enabled":true}`.
  The list is empty without an enabled configuration. It contains no
  credentials, issuer claims, or tenant data.
- `POST /api/v1/auth/providers/{id}/start` requires the exact configured
  HTTPS origin, existing browser CSRF and no bearer token. It returns
  `{"authorization_url":"..." }` for top-level navigation.
- The fixed `/api/v1/auth/providers/{id}/callback` accepts only the
  provider's method. State is tied to its provider and an HttpOnly Secure
  per-attempt browser cookie. The server consumes matching state once,
  with a five-minute deadline, before exchanging a code. The cookie
  is Lax for GET callbacks, and None for Apple's cross-site form POST.
- Go OAuth2 exchanges the code only at the pinned token URL. Go OIDC
  checks the signature with RS256 and pinned signing keys. Gomar checks
  exact issuer, client audience, nonce, issue time, expiration and, for
  Microsoft, exact tenant and key issuer scope. There is no arbitrary
  discovery URL, wildcard authority, outbound redirect or user-selected
  callback destination. Code, provider errors, profile and tokens are not
  returned to the browser. Successful login rotates Gomar's cookie and
  returns to `/` rather than a user-supplied link.
- The identity key is **provider, issuer, subject**. Email and IdP roles,
  groups and organizations never link an account or assign permission.
  First sign-in makes an ordinary account with an unusable password,
  no memberships, and no staff or superuser privilege. Repeat sign-in
  finds the same exact account. A disabled or privileged local row is
  not admitted through the provider sign-in path. App and MCP access
  continue to come from explicit Gomar roles and grants.

## Providers supported by the staged code

- **Google** (`google`): fixed HTTPS issuer, authorization/token/key URLs,
  code, nonce and S256 PKCE. This stage accepts the exact HTTPS issuer
  and does not silently normalize Google's legacy bare issuer form.
  [Google OIDC](https://developers.google.com/identity/openid-connect/openid-connect)
- **Apple** (`apple`): fixed Apple issuer and official REST endpoints,
  authorization code and form POST, nonce, no profile scopes. The code
  exchange uses a short-lived Services ID client-secret JWT supplied by
  the operator later. The REST authorization and token descriptions used
  here do not define S256 challenge or verifier fields. This adapter does
  not send unadvertised Apple PKCE parameters. It verifies the server
  exchange's identity token, not callback profile data.
  [Apple authorization](https://developer.apple.com/documentation/signinwithapplerestapi/request-an-authorization-to-the-sign-in-with-apple-server.),
  [Apple token validation](https://developer.apple.com/documentation/signinwithapplerestapi/generate-and-validate-tokens)
- **Microsoft personal** (`microsoft`): the documented `consumers`
  authority and exact consumer issuer/tenant GUID. **Microsoft work or
  school** (`microsoft-work`) optionally pins one configured directory
  GUID and its exact v2 issuer. They can coexist, with separate client
  configuration where required. Both use code, nonce and S256 PKCE.
  They cannot accept arbitrary tenants or the `common` or
  `organizations` authorities. A Microsoft signing key must be scoped to
  the exact issuer or to the documented tenant placeholder, resolved
  only against the configured tenant.
  [Microsoft authorization code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow),
  [Microsoft issuer and key validation](https://learn.microsoft.com/en-us/entra/identity-platform/access-tokens#validate-the-issuer)

## Runtime settings

This is the operator interface after migration 012 is registered and applied.
It is not a deployment instruction and no live configuration was added.

- `-enable-provider-auth` also requires the protected Unix HTTPS ingress.
  `GOMAR_AUTH_PUBLIC_ORIGIN` must be exactly `https://<unix-https-authority>`,
  with no trailing slash, path, query or alternate host. Incoming proxy
  headers cannot choose the callback origin.
- `GOMAR_AUTH_PROVIDERS` is an explicit comma-separated list, with no spaces,
  empty entries or duplicates: `google`, `apple`, `microsoft`, optionally
  `microsoft-work`. Only selected entries are advertised to the browser.
- For each selected entry, provide `<PREFIX>_CLIENT_ID` and
  `<PREFIX>_CLIENT_SECRET_FILE`, with prefixes `GOMAR_AUTH_GOOGLE`,
  `GOMAR_AUTH_APPLE`, `GOMAR_AUTH_MICROSOFT`, or `GOMAR_AUTH_MICROSOFT_WORK`.
  Set `GOMAR_AUTH_MICROSOFT_WORK_TENANT_ID` to the exact configured directory
  GUID for work or school sign-in. Do not put the secret itself in the
  environment or in command arguments. Secret files must be absolute,
  service-owned regular files, mode 0400 or 0600, not symlinks.
- The Apple secret file contains the short-lived ES256 client-secret JWT
  for the exact Services ID. Gomar checks its declared scope and time
  window offline. Apple still has to accept its signature and registration.
  [Apple secret troubleshooting](https://developer.apple.com/documentation/technotes/tn3107-resolving-sign-in-with-apple-response-errors)
- Startup validates inputs without contacting a provider. Outbound calls
  occur only during a valid browser flow. The server pins the endpoint,
  refuses redirects, does not inherit environment proxies, and never
  outputs credentials. Syntactic config checks are not a reachability test.

## Release gates

Register migration 012 after 011 and verify upgrades without changing
earlier checksums. Use the private credential path above. Do not put
provider secrets or private Apple signing keys in Git, frontend code,
logs or test data. Set ingress limits and avoid
logging callback query/body data. Prove Apple cookie behavior in real
browsers and each exact provider/client combination in an authorized
pilot. Protect existing users with an explicit account-link procedure,
not a lookup by email. Before removing normal password login, split the
administrator session and cookie into a distinct realm and verify that a
standard provider session never authenticates there, and vice versa.

Current tests use an in-memory transport with synthetic signatures, not
actual IdP accounts, endpoints, secrets or logins.


## Separate admin recovery (planned, not implemented)

Create a separate admin session table and cookie scoped to the admin realm.
Only existing active local superusers may sign in with their local passwords
there; normal provider sessions must not authorize admin routes. Admin cookie
tokens must never authorize app or MCP routes. Recheck the current local
privilege and password digest on every admin session; use short expiry,
session rotation, logout and origin/CSRF checks. Test both directions of
cookie rejection, inactive/non-superuser denial, session expiry and restore
before exposing an opt-in cutover. Do not convert existing user identities
or infer membership from provider or admin roles.

After the admin recovery rehearsal and owner-approved pilot show that each
provider to be offered is reachable and works for its intended account type,
seek a separate explicit cutover. Normal password auth on or off is a choice
for that later change; this branch enforces no cutover.
