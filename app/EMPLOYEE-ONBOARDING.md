# Employee sign-in preview

In Gomar, choose the intended organization and open **Administración →
Gestionar accesos → Empleados de la organización**. An administrator with
native access-management permission can invite a new employee into that organization and assign an existing
Compras, Contabilidad or Consulta role plus at least one of the
administrator's active locations. This is not a custom groups editor.
Existing account or provider identity linking is out of scope. Employees
with the same username at two different organizations are distinct accounts.

On Gomar’s sign-in page choose **Ingresar como empleado**, then enter the
organization number and employee username. An
administrator sees the initial temporary password once, then shares it
privately with that employee. It expires after 24 hours. Its first successful use consumes
it and allows only a separate, ten-minute password change. There is no staff
session until the employee replaces it. If they close the session or it
expires, the administrator must issue a new temporary password.
The employee's stable internal record is not a platform login, provider
identity or administrator recovery account.

Keep paying-customer registration separate. First settle who can create
and own an organization, then verify paid activation against a trusted
billing record from the private Turnate system before creating the
organization or assigning owner access. A browser return URL or claim that
someone paid is not evidence. Billing identity must not become an employee
account or grant itself organization permissions. This preview has no
public signup, billing integration, live payments or provider calls.

The feature is off by default. For a staged local preview,
`-enable-employee-onboarding` also leaves existing ordinary password sign-in
available. Keep it off for public deployments.

Use `-provider-employee-sign-in` for the separate, strict mode. It requires
`-enable-provider-auth`, complete Google and Microsoft personal account
configuration, protected Unix HTTPS ingress, and `-enable-admin-recovery-realm`
with an existing local staff superuser. It enables managed employees and
accepts only verified provider sessions or managed employee sessions on app
routes; the general local password form and endpoint are disabled. The
platform administrator uses the separate `/admin/login/` recovery realm.
Provider identities still need explicit organization grants. Existing MCP
keys bound to ordinary local-password accounts are rejected in this mode;
managed employees cannot get MCP keys. The mode does not delete those old
sessions or keys. Reverting to compatible mode can re-enable unexpired
ones; revoke them separately if needed. The original
`-provider-only-sign-in` is unchanged and rejects either employee flag.

Employees use the standard browser features allowed by their native role.
This first release does not issue any MCP key to employee accounts; earlier
browser and MCP behavior for other accounts is unchanged. If the employee
feature is turned off, existing employee cookies are not accepted.
A queued offline POS sale is not a posted sale. If the employee loses access
before it reaches the server, signing that queued item does not restore it.

Employee sign-in and password replacement have a bounded per-process
attempt budget and concurrent password-check limit. Deployments must still
apply their normal ingress limits and monitoring. The shared process-wide limit reserves no slot per organization. Limits are
not shared across processes or restarts. Before hosted rollout, rate-limit
the employee authentication endpoints at ingress and check different
organizations can sign in during ordinary peak traffic. No credentials are
logged by the app.

New access can include only active locations. Reset and deactivation use a
conservative check of the employee's complete grant history, including
inactive locations. If an administrator no longer covers that scope,
another authorized administrator must manage the account.
