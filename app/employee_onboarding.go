package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func employeeSignInFlags(preview, providerOnly, providerEmployee, providers, recovery bool) error {
	if providerOnly && (preview || providerEmployee) {
		return errors.New("provider-only sign-in cannot be combined with employee sign-in")
	}
	if (providerOnly || providerEmployee) && (!providers || !recovery) {
		return errProviderOnlyUnsafe
	}
	return nil
}

func (a *app) managedEmployeesEnabled() bool {
	return !a.providerOnly && (a.employeeOnboarding || a.providerEmployee)
}

func (a *app) classicPasswordsDisabled() bool {
	return a.providerOnly || a.providerEmployee
}

const employeeAnchorPrefix = "!employee-v1-"

func isEmployeeAnchor(password string) bool { return strings.HasPrefix(password, employeeAnchorPrefix) }

const employeeAuthBase = "/api/v1/auth/employee"
const employeeChangeCookie = "gomar_employee_change"
const employeeChangeAge = 10 * time.Minute
const employeeTemporaryAge = 24 * time.Hour

var employeeHandle = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,47}$`)

func registerEmployeeOnboarding(a *app, add func(string, string, http.HandlerFunc)) {
	if !a.managedEmployeesEnabled() {
		return
	}
	add(employeeAuthBase+"/login", http.MethodPost, a.employeeAuthRoute(a.employeeLogin))
	add(employeeAuthBase+"/password", http.MethodPost, a.employeeAuthRoute(a.employeeChangePassword))
	add("/api/v1/entities/{entity}/employees", http.MethodGet, a.scoped("manage_entity_access", a.listEmployees))
	add("/api/v1/entities/{entity}/employees", http.MethodPost, a.scopedEmployeeManagement(a.inviteEmployee))
	add("/api/v1/entities/{entity}/employees/{employee}/reset", http.MethodPost, a.scopedEmployeeManagement(a.resetEmployee))
	add("/api/v1/entities/{entity}/employees/{employee}/disable", http.MethodPost, a.scopedEmployeeManagement(a.disableEmployee))
}

func (a *app) scopedEmployeeManagement(next scopedHandler) http.HandlerFunc {
	return a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		id, err := pathID(r, "entity")
		if err != nil {
			return err
		}
		body, err := readJSONBody(w, r)
		if err != nil {
			return err
		}
		tx, err := a.db.BeginTx(r.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
			return err
		}
		if _, err = scopedMembership(tx, u.id, id, "manage_entity_access"); err != nil {
			return err
		}
		// An employee cannot be an administrator. Lock target before
		// domain locks, matching the employee's native writes.
		if r.PathValue("employee") != "" {
			target, err := pathID(r, "employee")
			if err != nil {
				return err
			}
			var found int64
			err = tx.QueryRowContext(r.Context(), `SELECT u.id FROM users u JOIN organization_employees e ON e.user_id=u.id
    WHERE u.id=$1 AND e.entity_id=$2 AND NOT u.staff AND NOT u.superuser FOR UPDATE OF u`, target, id).Scan(&found)
			if errors.Is(err, sql.ErrNoRows) {
				return missingEntity
			}
			if err != nil {
				return err
			}
		}
		if err = lockPostgresEntity(r.Context(), tx, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return missingEntity
			}
			return err
		}
		m, err := scopedMembership(tx, u.id, id, "manage_entity_access")
		if err != nil {
			return err
		}
		status, result, err := next(r, tx, m, body)
		if err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, status, result)
		return nil
	})
}

func readEmployeeJSON(w http.ResponseWriter, r *http.Request, target any, fields ...string) error {
	body, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	if len(body) > 2048 {
		return badInput
	}
	return decodeWikiProposalJSON(body, target, fields...)
}

func (a *app) employeeAuthRoute(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.RawQuery != "" || !validCSRF(r) {
			failure(w, badCSRF)
			return
		}
		if err := fn(w, r); err != nil {
			failure(w, err)
		}
	}
}

func setEmployeeChangeCookie(w http.ResponseWriter, r *http.Request, value string, age time.Duration) {
	c := &http.Cookie{Name: employeeChangeCookie, Value: value, Path: employeeAuthBase + "/password", HttpOnly: true, Secure: effectiveHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: int(age.Seconds())}
	if age == 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

type employeeInviteReply struct {
	ID                int64  `json:"id"`
	Organization      int64  `json:"organization"`
	Username          string `json:"username"`
	TemporaryPassword string `json:"temporary_password"`
	ExpiresAt         string `json:"expires_at"`
}

func employeeOutlets(tx *sql.Tx, scope entityMembership) ([]int64, error) {
	// The native organization access mutation holds the entity lock and
	// checks current permission. Inactive or foreign branches cannot be assigned.
	rows, err := tx.Query(`SELECT o.id FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
  WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.active ORDER BY o.id`, scope.id, scope.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func employeeInviteReplyFor(id, entity int64, handle, tmp string, expiry time.Time) employeeInviteReply {
	return employeeInviteReply{id, entity, handle, tmp, expiry.UTC().Format(time.RFC3339)}
}
func employeeAudit(tx *sql.Tx, id, actor int64, action string, now int64) error {
	_, err := tx.Exec(`INSERT INTO organization_employee_events(employee_id,actor_id,action,occurred_at) VALUES($1,$2,$3,$4)`, id, actor, action, now)
	return err
}

type managedEmployee struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Status   string `json:"status"`
}

func (a *app) listEmployees(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	var before int64
	var enabled bool
	if r.URL.RawQuery != "" {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 1 || len(query["before"]) != 1 {
			return 0, nil, badInput
		}
		group, id, found := strings.Cut(query.Get("before"), "-")
		before, err = strconv.ParseInt(id, 10, 64)
		if !found || err != nil || before < 1 || strconv.FormatInt(before, 10) != id || (group != "a" && group != "d") {
			return 0, nil, badInput
		}
		enabled = group == "a"
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT e.user_id,e.handle,t.role,e.status
  FROM organization_employees e JOIN memberships t ON (t.user_id,t.entity_id)=(e.user_id,e.entity_id)
  WHERE e.entity_id=$1 AND NOT EXISTS (
   SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id WHERE g.membership_id=t.id
    AND (o.entity_id<>$1 OR NOT EXISTS(SELECT 1 FROM outlet_grants actor WHERE actor.membership_id=$2 AND actor.outlet_id=g.outlet_id)))
  AND ($4::bigint=0 OR (e.status<>'disabled',e.user_id) < ($3::boolean,$4))
 ORDER BY (e.status<>'disabled') DESC,e.user_id DESC LIMIT 101`, m.ID, m.id, enabled, before)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	result := []managedEmployee{}
	for rows.Next() {
		var item managedEmployee
		if err := rows.Scan(&item.ID, &item.Username, &item.Role, &item.Status); err != nil {
			return 0, nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	reply := map[string]any{"employees": result, "truncated": false}
	if len(result) > 100 {
		last := result[99]
		group := "a-"
		if last.Status == "disabled" {
			group = "d-"
		}
		reply["employees"] = result[:100]
		reply["truncated"] = true
		reply["next_cursor"] = group + strconv.FormatInt(last.ID, 10)
	}
	return 200, reply, nil
}

func (a *app) inviteEmployee(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var in struct {
		Username string  `json:"username"`
		Role     string  `json:"role"`
		Outlets  []int64 `json:"outlet_ids"`
	}
	if len(body) > 2048 {
		return 0, nil, badInput
	}
	if err := decodeWikiProposalJSON(body, &in, "username", "role", "outlet_ids"); err != nil {
		return 0, nil, err
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if !employeeHandle.MatchString(in.Username) || len(in.Outlets) == 0 || len(in.Outlets) > 64 {
		return 0, nil, badInput
	}
	switch in.Role {
	case "purchasing", "accounting", "read_only":
	default:
		return 0, nil, forbiddenRole
	}
	if !containsCapabilities(capabilities[m.role], capabilities[in.Role]) {
		return 0, nil, forbiddenRole
	}
	allowed, err := employeeOutlets(tx, m)
	if err != nil {
		return 0, nil, err
	}
	slices.Sort(in.Outlets)
	if len(slices.Compact(slices.Clone(in.Outlets))) != len(in.Outlets) {
		return 0, nil, badInput
	}
	if !containsAll(allowed, in.Outlets) {
		return 0, nil, missingOutlet
	}
	var id, member int64
	if err = tx.QueryRow(`INSERT INTO users(username,password,active,staff,superuser)
  VALUES($1,$2,FALSE,FALSE,FALSE) RETURNING id`, "employee-"+randomToken(), employeeAnchorPrefix+randomToken()).Scan(&id); err != nil {
		return 0, nil, err
	}
	if err = tx.QueryRow(`INSERT INTO memberships(user_id,entity_id,role,active) VALUES($1,$2,$3,TRUE) RETURNING id`, id, m.ID, in.Role).Scan(&member); err != nil {
		return 0, nil, err
	}
	for _, outlet := range in.Outlets {
		if _, err = tx.Exec(`INSERT INTO outlet_grants(membership_id,outlet_id) VALUES($1,$2)`, member, outlet); err != nil {
			return 0, nil, err
		}
	}
	now := time.Now()
	expiry := now.Add(employeeTemporaryAge)
	tmp := randomToken()
	if _, err = tx.Exec(`INSERT INTO organization_employees(user_id,entity_id,handle,status,temporary_hash,temporary_expires_at)
  VALUES($1,$2,$3,'invited',$4,$5)`, id, m.ID, in.Username, encodePassword(tmp), expiry.Unix()); isUnique(err) {
		return 0, nil, problem{409, "Ese usuario ya existe en esta organización."}
	} else if err != nil {
		return 0, nil, err
	}
	after, _ := json.Marshal(accessSnapshot{in.Role, true, in.Outlets})
	if _, err = tx.Exec(`INSERT INTO access_events(entity_id,membership_id,actor_id,action,before,after,occurred_at)
  VALUES($1,$2,$3,'create_membership','{}',$4,$5)`, m.ID, member, m.userID, string(after), now.UTC().Format(time.RFC3339Nano)); err != nil {
		return 0, nil, err
	}
	if err = employeeAudit(tx, id, m.userID, "invited", now.Unix()); err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, employeeInviteReplyFor(id, m.ID, in.Username, tmp, expiry), nil
}

func containsCapabilities(have, requested []string) bool {
	for _, c := range requested {
		if !slices.Contains(have, c) {
			return false
		}
	}
	return true
}

func (a *app) resetEmployee(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	return a.changeEmployeeState(r, tx, m, body, true)
}
func (a *app) disableEmployee(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	return a.changeEmployeeState(r, tx, m, body, false)
}
func (a *app) changeEmployeeState(r *http.Request, tx *sql.Tx, m entityMembership, body []byte, reset bool) (int, any, error) {
	var input struct{}
	if err := decodeWikiProposalJSON(body, &input); err != nil {
		return 0, nil, err
	}
	id, err := pathID(r, "employee")
	if err != nil {
		return 0, nil, err
	}
	var member int64
	var handle, role, priorStatus string
	err = tx.QueryRow(`SELECT t.id,e.handle,t.role,e.status FROM organization_employees e
  JOIN memberships t ON (t.user_id,t.entity_id)=(e.user_id,e.entity_id)
  JOIN users u ON u.id=e.user_id WHERE e.user_id=$1 AND e.entity_id=$2 AND NOT u.staff AND NOT u.superuser
  FOR UPDATE OF u,e`, id, m.ID).Scan(&member, &handle, &role, &priorStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, missingEntity
	}
	if err != nil {
		return 0, nil, err
	}
	if reset && priorStatus == "disabled" {
		return 0, nil, problem{409, "La cuenta está desactivada."}
	}
	if !containsCapabilities(capabilities[m.role], capabilities[role]) || role == "administration" {
		return 0, nil, forbiddenRole
	}
	old, valid, err := grantScope(tx, member, m.ID)
	if err != nil {
		return 0, nil, err
	}
	own, _, err := grantScope(tx, m.id, m.ID)
	if err != nil {
		return 0, nil, err
	}
	if !valid || !containsAll(own, old) {
		return 0, nil, forbiddenRole
	}
	if _, err = tx.Exec(`UPDATE users SET active=FALSE,password=$2 WHERE id=$1`, id, employeeAnchorPrefix+randomToken()); err != nil {
		return 0, nil, err
	}
	if _, err = tx.Exec(`DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
		return 0, nil, err
	}
	now := time.Now()
	var tmp string
	var hash any
	var expires any
	status, action := "disabled", "disabled"
	expiry := now.Add(employeeTemporaryAge)
	if reset {
		tmp = randomToken()
		hash = encodePassword(tmp)
		expires = expiry.Unix()
		status, action = "invited", "reset"
	}
	if _, err = tx.Exec(`UPDATE organization_employees SET status=$2,password_hash=NULL,
  temporary_hash=$3,temporary_expires_at=$4,change_hash=NULL,change_expires_at=NULL,change_previous_hash=NULL WHERE user_id=$1`, id, status, hash, expires); err != nil {
		return 0, nil, err
	}
	if err = employeeAudit(tx, id, m.userID, action, now.Unix()); err != nil {
		return 0, nil, err
	}
	if reset {
		return 200, employeeInviteReplyFor(id, m.ID, handle, tmp, expiry), nil
	}
	return 200, map[string]string{"status": "disabled"}, nil
}

func (a *app) employeeLogin(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Organization int64  `json:"organization"`
		Username     string `json:"username"`
		Password     string `json:"password"`
	}
	if err := readEmployeeJSON(w, r, &input, "organization", "username", "password"); err != nil {
		return err
	}
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	if input.Organization <= 0 || !employeeHandle.MatchString(input.Username) || len(input.Password) == 0 || len(input.Password) > 256 {
		return badInput
	}
	if cookieValue(r, "sessionid") != "" {
		return problem{409, "Cerrá la sesión actual antes de ingresar."}
	}
	if !a.employeePasswords.enter(employeeLoginBudgetKey(input.Organization, input.Username), time.Now()) {
		return employeeAuthBusy
	}
	defer a.employeePasswords.leave()
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var status, core string
	var active bool
	var permanent, temporary sql.NullString
	var expiry sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.password,u.active,c.status,c.password_hash,c.temporary_hash,c.temporary_expires_at
  FROM organization_employees c JOIN users u ON u.id=c.user_id
  JOIN memberships m ON (m.user_id,m.entity_id)=(c.user_id,c.entity_id)
  JOIN entities e ON e.id=c.entity_id
  WHERE c.entity_id=$1 AND c.handle=$2 AND m.active AND e.active AND NOT u.staff AND NOT u.superuser
  FOR UPDATE OF u,c`, input.Organization, input.Username).Scan(&id, &core, &active, &status, &permanent, &temporary, &expiry)
	denied := problem{401, incorrectLogin}
	if errors.Is(err, sql.ErrNoRows) {
		verifyPassword("", input.Password)
		return denied
	}
	if err != nil {
		return err
	}
	hash := permanent.String
	if status == "invited" {
		hash = temporary.String
	}
	if !verifyPassword(hash, input.Password) {
		return denied
	}
	now := time.Now()
	if status == "invited" && !active && expiry.Valid && now.Unix() < expiry.Int64 {
		token := randomToken()
		key := sha256.Sum256([]byte(token))
		if _, err = tx.ExecContext(ctx, `UPDATE organization_employees SET status='changing_password',change_previous_hash=temporary_hash,temporary_hash=NULL,
   temporary_expires_at=NULL,change_hash=$2,change_expires_at=$3 WHERE user_id=$1`, id, key[:], now.Add(employeeChangeAge).Unix()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		setEmployeeChangeCookie(w, r, token, employeeChangeAge)
		jsonResponse(w, 200, map[string]string{"status": "change_password"})
		return nil
	}
	if status != "active" || !active {
		return denied
	}
	token := randomToken()
	key := sha256.Sum256([]byte(token))
	digest := sha256.Sum256([]byte(core))
	if err := requireSessionCapacity(ctx, tx, id, now.Unix(), false); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,password_digest,expires,auth_method) VALUES($1,$2,$3,$4,'employee')`, key[:], id, digest[:], now.Add(sessionAge).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET last_login=$2 WHERE id=$1`, id, now.Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	setCookie(w, r, "sessionid", token, sessionAge, true)
	setCookie(w, r, "csrftoken", randomToken(), csrfAge, false)
	jsonResponse(w, 200, map[string]string{"status": "authenticated", "username": input.Username})
	return nil
}

func (a *app) employeeChangePassword(w http.ResponseWriter, r *http.Request) error {
	token := cookieValue(r, employeeChangeCookie)
	if len(token) != 32 || cookieValue(r, "sessionid") != "" {
		return unauthenticated
	}
	var input struct {
		Password string `json:"password"`
	}
	if err := readEmployeeJSON(w, r, &input, "password"); err != nil {
		return err
	}
	if !utf8.ValidString(input.Password) || len(input.Password) > 256 || utf8.RuneCountInString(input.Password) < 12 {
		return problem{422, "Usá una contraseña de al menos 12 caracteres."}
	}
	key := sha256.Sum256([]byte(token))
	budget := sha256.Sum256([]byte("change/" + token))
	if !a.employeePasswords.enter(budget, time.Now()) {
		return employeeAuthBusy
	}
	defer a.employeePasswords.leave()
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var handle, previousHash string
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT u.id,c.handle,c.change_expires_at,c.change_previous_hash FROM organization_employees c
  JOIN users u ON u.id=c.user_id JOIN memberships m ON (m.user_id,m.entity_id)=(c.user_id,c.entity_id)
  JOIN entities e ON e.id=c.entity_id
  WHERE c.change_hash=$1 AND c.status='changing_password' AND m.active AND e.active AND NOT u.active AND NOT u.staff AND NOT u.superuser
  FOR UPDATE OF u,c`, key[:]).Scan(&id, &handle, &expires, &previousHash)
	if errors.Is(err, sql.ErrNoRows) {
		return unauthenticated
	}
	if err != nil {
		return err
	}
	if len(validateNewAdminPassword(input.Password, handle)) != 0 {
		return problem{422, "Elegí una contraseña distinta del usuario, que no sea común ni solo numérica."}
	}
	if expires <= time.Now().Unix() {
		return unauthenticated
	}
	if verifyPassword(previousHash, input.Password) {
		return problem{422, "La contraseña nueva tiene que ser distinta de la temporal."}
	}
	encoded := encodePassword(input.Password)
	now := time.Now()
	if expires <= now.Unix() {
		return unauthenticated
	}
	if _, err = tx.ExecContext(ctx, `UPDATE organization_employees SET status='active',password_hash=$2,change_hash=NULL,change_expires_at=NULL,change_previous_hash=NULL WHERE user_id=$1`, id, encoded); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET active=TRUE WHERE id=$1`, id); err != nil {
		return err
	}
	if err = employeeAudit(tx, id, id, "activated", now.Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	setEmployeeChangeCookie(w, r, "", 0)
	// Require an explicit fresh login. No staff cookie is issued here.
	jsonResponse(w, 200, map[string]string{"status": "password_changed"})
	return nil
}
