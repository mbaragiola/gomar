// Package mcpclient reads authenticated Gomar data over a fixed, typed API.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxResponse = 128 * 1024

var invalidResponse = errors.New("Gomar devolvió una respuesta inválida")

type Client struct {
	origin url.URL
	token  string
	http   *http.Client
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Named struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Entities struct {
	Entities []Named `json:"entities"`
}
type Outlets struct {
	Outlets []Named `json:"outlets"`
}

type OutletSales struct {
	Named
	SaleCount  int64 `json:"sale_count"`
	TotalMinor int64 `json:"total_minor"`
}

type Sales struct {
	Period     string        `json:"period"`
	Timezone   string        `json:"timezone"`
	From       string        `json:"from"`
	To         string        `json:"to"`
	Currency   string        `json:"currency"`
	SaleCount  int64         `json:"sale_count"`
	TotalMinor int64         `json:"total_minor"`
	Outlets    []OutletSales `json:"outlets"`
}

func validToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(b) == 32
}

// ReadToken accepts a private regular file, not a symlink. Neither the file
// contents nor its location are returned to the MCP host.
func ReadToken(path string) (string, error) {
	bad := errors.New("usá un archivo de credencial privado, sin permisos para otras cuentas")
	prior, err := os.Lstat(path)
	if err != nil || !prior.Mode().IsRegular() || prior.Mode().Perm()&0o077 != 0 {
		return "", bad
	}
	f, err := os.Open(path)
	if err != nil {
		return "", bad
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(prior, actual) || actual.Mode().Perm()&0o077 != 0 {
		return "", bad
	}
	raw, err := io.ReadAll(io.LimitReader(f, 46))
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if err != nil || !validToken(value) {
		return "", errors.New("la credencial de Gomar no tiene el formato esperado")
	}
	return value, nil
}

func New(origin, token string) (*Client, error) {
	base, err := url.Parse(origin)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawPath != "" ||
		(base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" ||
		strings.Contains(base.Hostname(), "%") {
		return nil, errors.New("indicá sólo el origen HTTPS de Gomar, sin ruta ni credenciales")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "::1")) {
		return nil, errors.New("se requiere HTTPS, salvo 127.0.0.1 o [::1] para pruebas locales")
	}
	if !validToken(token) {
		return nil, errors.New("la credencial de Gomar no tiene el formato esperado")
	}
	// No ambient proxies, cookie jar, or insecure TLS overrides.
	tr := &http.Transport{
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
		MaxIdleConnsPerHost: 2, IdleConnTimeout: 15 * time.Second, MaxResponseHeaderBytes: 16 * 1024,
	}
	return &Client{origin: *base, token: token, http: &http.Client{
		Transport: tr, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Me(ctx context.Context) (User, error) {
	var result User
	err := c.get(ctx, "/api/v1/mcp/me", nil, &result)
	if err == nil && (result.ID <= 0 || strings.TrimSpace(result.Username) == "") {
		err = invalidResponse
	}
	return result, err
}

func (c *Client) Entities(ctx context.Context) (Entities, error) {
	var result Entities
	err := c.get(ctx, "/api/v1/mcp/entities", nil, &result)
	if err == nil && !validPlaces(result.Entities) {
		err = invalidResponse
	}
	return result, err
}

func (c *Client) Outlets(ctx context.Context, entity int64) (Outlets, error) {
	var result Outlets
	if entity < 1 {
		return result, errors.New("indicá un entity_id devuelto por gomar_list_entities")
	}
	err := c.get(ctx, entityPath(entity)+"/outlets", nil, &result)
	if err == nil && !validPlaces(result.Outlets) {
		err = invalidResponse
	}
	return result, err
}

func (c *Client) Sales(ctx context.Context, entity int64, outlet *int64, period string) (Sales, error) {
	var result Sales
	if entity < 1 {
		return result, errors.New("indicá un entity_id devuelto por gomar_list_entities")
	}
	if period != "this_month" && period != "previous_month" {
		return result, errors.New("usá this_month o previous_month")
	}
	query := url.Values{"period": {period}}
	if outlet != nil {
		if *outlet < 1 {
			return result, errors.New("indicá un outlet_id devuelto por gomar_list_outlets u omitilo")
		}
		query.Set("outlet_id", strconv.FormatInt(*outlet, 10))
	}
	err := c.get(ctx, entityPath(entity)+"/sales-summary", query, &result)
	if err == nil && !validSales(result, period, outlet) {
		err = invalidResponse
	}
	return result, err
}

func validPlaces(places []Named) bool {
	if places == nil {
		return false
	}
	seen := map[int64]bool{}
	for _, p := range places {
		if p.ID <= 0 || strings.TrimSpace(p.Name) == "" || seen[p.ID] {
			return false
		}
		seen[p.ID] = true
	}
	return true
}

func validSales(s Sales, period string, requested *int64) bool {
	from, e1 := time.Parse(time.RFC3339, s.From)
	to, e2 := time.Parse(time.RFC3339, s.To)
	if e1 != nil || e2 != nil || !from.Before(to) || s.Timezone == "" || s.Period != period || s.Currency != "ARS" || s.Outlets == nil {
		return false
	}
	var count, total int64
	places := make([]Named, 0, len(s.Outlets))
	for _, o := range s.Outlets {
		if requested != nil && o.ID != *requested {
			return false
		}
		if o.SaleCount < 0 || o.TotalMinor < 0 || (o.SaleCount == 0) != (o.TotalMinor == 0) ||
			count > math.MaxInt64-o.SaleCount || total > math.MaxInt64-o.TotalMinor {
			return false
		}
		count += o.SaleCount
		total += o.TotalMinor
		places = append(places, o.Named)
	}
	return validPlaces(places) && s.SaleCount == count && s.TotalMinor == total
}

func entityPath(entity int64) string { return "/api/v1/mcp/entities/" + strconv.FormatInt(entity, 10) }

func (c *Client) get(ctx context.Context, path string, query url.Values, dst any) error {
	u := c.origin
	u.Path = path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("no se pudo preparar la consulta a Gomar")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("no se pudo conectar a Gomar")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return errors.New("la credencial de Gomar no es válida o venció")
	case http.StatusForbidden, http.StatusNotFound:
		return errors.New("la consulta no está disponible con tus permisos de Gomar")
	default:
		return fmt.Errorf("Gomar no pudo responder (HTTP %d)", resp.StatusCode)
	}
	format, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || format != "application/json" {
		return errors.New("Gomar no devolvió JSON")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return errors.New("la respuesta de Gomar no es válida o supera el límite")
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, dst) != nil {
		return invalidResponse
	}
	return nil
}
