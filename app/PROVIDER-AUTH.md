# Provider sign-in foundation

This change is **off by default**. The normal executable does not load
provider settings. Existing password login and administrator access are
unchanged. Migration `012_provider_auth.sql` is staged but not in the
migrator until the public sequence contains 011. Tests apply the staged
SQL on disposable PostgreSQL. Do not turn on provider login before
registration, browser verification, and the administrator-session split.

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

## Release gates

Register migration 012 after 011 and verify upgrades without changing
earlier checksums. Define a secure credential loading path before
enabling any provider; do not put Secrets or Service ID certificates in
Git, frontend code, logs or test data. Set ingress limits and avoid
logging callback query/body data. Prove Apple cookie behavior in real
browsers and each exact provider/client combination in an authorized
pilot. Protect existing users with an explicit account-link procedure,
not a lookup by email. Before removing normal password login, split the
administrator session and cookie into a distinct realm and verify that a
standard provider session never authenticates there, and vice versa.

Current tests use an in-memory transport with synthetic signatures, not
actual IdP accounts, endpoints, secrets or logins.
