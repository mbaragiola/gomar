# Provider sign-in foundation

This change is **off by default**. The executable reads no provider
settings unless `-enable-provider-auth` is explicitly selected. Existing
password login and administrator access are unchanged. Migrations 018 and
019 register the provider and admin recovery storage after 017. Do not
set the opt-in flags until the release gates below pass.

## Flow and identity

- `GET /api/v1/auth/providers` returns
  `{"providers":[{"id":"google","label":"Google"}],"local_password_enabled":true}`.
  The list is empty without an enabled configuration. The reply also
  contains `admin_recovery_enabled`, true only for the protected recovery
  realm. It contains no credentials, issuer claims, or tenant data.
- `POST /api/v1/auth/providers/{id}/start` requires the exact configured
  HTTPS origin, existing browser CSRF and no bearer token. It returns
  `{"authorization_url":"..." }` for top-level navigation.
- The fixed `/api/v1/auth/providers/{id}/callback` accepts only the
  provider's method. State is tied to its provider and an HttpOnly Secure
  per-attempt browser cookie. The server consumes matching state once,
  with a five-minute deadline, before exchanging a code. The cookie
  is Lax for the supported GET callbacks.
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

## Apple is disabled

Apple is not an accepted provider setting, no Apple login routes are
registered, and Gomar does not display an Apple sign-in option. The
inactive prototype adapter is not a launch commitment. Apple documents
that web authentication requires an existing App Store app that uses Sign in
with Apple; Gomar has no eligible app. No Apple account or credentials are
required for provider-only mode.
[Apple requirements](https://developer.apple.com/documentation/signinwithapple/configuring-your-environment-for-sign-in-with-apple)

## Runtime settings

This is the operator interface after migration 018 is registered and applied.
It is not a deployment instruction and no live configuration was added.

- `-enable-provider-auth` also requires the protected Unix HTTPS ingress.
  `GOMAR_AUTH_PUBLIC_ORIGIN` must be exactly `https://<unix-https-authority>`,
  with no trailing slash, path, query or alternate host. Incoming proxy
  headers cannot choose the callback origin. A stable auth callback host
  per hosted environment is a design option, not a deployed configuration.
  Review session handoff separately before adding any other origin.
- `GOMAR_AUTH_PROVIDERS` is an explicit comma-separated list, with no spaces,
  empty entries or duplicates: `google`, `microsoft`, optionally
  `microsoft-work`. Only selected entries are advertised to the browser.
- For each selected entry, provide `<PREFIX>_CLIENT_ID` and
  `<PREFIX>_CLIENT_SECRET_FILE`, with prefixes `GOMAR_AUTH_GOOGLE`,
  `GOMAR_AUTH_MICROSOFT`, or `GOMAR_AUTH_MICROSOFT_WORK`.
  Set `GOMAR_AUTH_MICROSOFT_WORK_TENANT_ID` to the exact configured directory
  GUID for work or school sign-in. Do not put the secret itself in the
  environment or in command arguments. Secret files must be absolute,
  service-owned regular files, mode 0400 or 0600, not symlinks.
- Startup validates inputs without contacting a provider. Outbound calls
  occur only during a valid browser flow. The server pins the endpoint,
  refuses redirects, does not inherit environment proxies, and never
  outputs credentials. Syntactic config checks are not a reachability test.

## Release gates

Verify migration 018 and 019 upgrades without changing
earlier checksums. Use the private credential path above. Do not put
provider secrets in Git, frontend code,
logs or test data. Set ingress limits and avoid
logging callback query/body data. Verify each exact provider/client combination in an authorized
pilot. Protect existing users with an explicit account-link procedure,
not a lookup by email. Before removing normal password login, split the
administrator session and cookie into a distinct realm and verify that a
standard provider session never authenticates there, and vice versa.

Current tests use an in-memory transport with synthetic signatures, not
actual IdP accounts, endpoints, secrets or logins.


## Staged admin recovery and provider-only sign-in

Neither flag is enabled by default:

- `-enable-admin-recovery-realm`: protects the existing `/admin/login/` with a
  separate, 30-minute password session. Requires registered/applied
  migration 019, protected Unix HTTPS ingress, and an existing active local
  account with both staff and superuser flags and a usable password hash.
  No account is created or changed at startup.
- `-provider-only-sign-in`: additionally requires the recovery flag,
  `-enable-provider-auth`, registered/applied 018 and 019, and complete
  Google and Microsoft **personal** configurations. Optional work
  Microsoft cannot stand in for personal. Classic password sign-in is
  blocked in the app, and classic password sessions no longer authorize
  ordinary app routes in this mode. Existing records are retained so the
  default compatible mode does not invalidate them on migration alone.
  Client-provided identity claims still cannot supply Gomar permissions.

The admin password path accepts only existing active local staff
superusers. It uses separate admin-only Secure, HttpOnly, SameSite=Strict
session and CSRF cookies under `/admin/`. Recovery does not issue an app
session and cannot mint MCP credentials. Provider app sessions cannot
access the admin panel. Password changes rotate the recovery token;
logout deletes its row and clears only the recovery cookies. Native
permission, origin and CSRF checks remain in effect.

Before using either flag outside the synthetic harness, land the migrations
in order, rehearse recovery locally with both cookie directions denied, and
agree on how existing permitted people get explicit Gomar access. Before
an explicit cutover, confirm the authorized pilot for every offered account
type against the provider itself. Startup checks are offline and cannot
prove that a real client registration or provider is reachable.

Plan how to let existing MCP links expire or revoke them before switching.
Rejecting password cookies on ordinary app routes does not immediately revoke
every previously issued MCP key. Test any offline POS queue with the chosen
session transition before applying it. An older binary refuses an unknown
schema version, so keep a schema-compatible rollback or a restore plan.

This branch did not enroll anyone, switch sign-in modes in a running
deployment, or contact providers.
