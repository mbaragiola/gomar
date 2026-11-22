package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var proposalRequestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ProposalClient can queue a single kind of proposal. It cannot approve one.
type ProposalClient struct{ transport *Client }

func NewProposalClient(origin, token string) (*ProposalClient, error) {
	transport, err := New(origin, token)
	if err != nil {
		return nil, err
	}
	return &ProposalClient{transport: transport}, nil
}

type ProposalReceipt struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

type WikiCreateReceipt = ProposalReceipt

func (c *ProposalClient) ProposeWikiCreate(ctx context.Context, entity, outlet int64, requestID, title, body string) (WikiCreateReceipt, error) {
	var out WikiCreateReceipt
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) {
		return out, errors.New("indicá la empresa, sucursal y un request_id UUIDv4")
	}
	title = strings.TrimSpace(title)
	if !validWikiProposalText(title, body) {
		return out, errors.New("revisá que el título y el contenido respeten los límites de la wiki")
	}
	payload, err := json.Marshal(struct {
		RequestID string `json:"request_id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
	}{requestID, title, body})
	if err != nil {
		return out, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/wiki-create-proposals", payload)
}

func validWikiProposalText(title, body string) bool {
	return title != "" && strings.TrimSpace(body) != "" && utf8.ValidString(title) && utf8.ValidString(body) &&
		utf8.RuneCountInString(title) <= 120 && utf8.RuneCountInString(body) <= 12000 &&
		len(body) <= 48*1024 && !strings.ContainsRune(title, 0) && !strings.ContainsRune(body, 0)
}

// path is a compile-time suffix from a typed proposal method, never tool input.
func (c *ProposalClient) postProposal(ctx context.Context, entity, outlet int64, path string, payload []byte) (ProposalReceipt, error) {
	var out ProposalReceipt
	u := c.transport.origin
	u.Path = entityPath(entity) + "/outlets/" + strconv.FormatInt(outlet, 10) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return out, errors.New("no se pudo preparar la propuesta")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.transport.token)
	resp, err := c.transport.http.Do(req)
	if err != nil {
		return out, errors.New("no se pudo conectar a Gomar")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
	case http.StatusUnauthorized:
		return out, errors.New("la clave de propuestas no es válida o venció")
	case http.StatusForbidden, http.StatusNotFound:
		return out, errors.New("la propuesta no está disponible con tus permisos de Gomar")
	case http.StatusConflict:
		switch path {
		case "/wiki-edit-proposals":
			return out, errors.New("la página o la propuesta cambió; volvé a leer la página y usá un request_id nuevo si cambia el contenido")
		case "/stock-receipt-proposals":
			return out, errors.New("revisá que el producto siga activo y con stock habilitado en esta sucursal; usá un request_id nuevo si cambia el contenido")
		case "/pos-rename-proposals":
			return out, errors.New("revisá la revisión y el nombre actual del producto; usá un request_id nuevo si cambia el contenido")
		case "/document-review-proposals":
			return out, errors.New("volvé a leer el borrador y su revisión antes de proponer la revisión; usá un request_id nuevo si cambia el contenido")
		case "/document-edit-proposals":
			return out, errors.New("volvé a leer el borrador y su revisión antes de proponer el cambio; usá un request_id nuevo si cambia el contenido")
		}
		return out, errors.New("el request_id ya se usó con otro contenido")
	case http.StatusUnprocessableEntity:
		return out, errors.New("Gomar rechazó los campos de la propuesta")
	case http.StatusTooManyRequests:
		return out, errors.New("alcanzaste el límite de propuestas de Gomar")
	default:
		return out, errors.New("Gomar no pudo guardar la propuesta")
	}
	format, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || format != "application/json" {
		return out, invalidResponse
	}
	result, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(result) > 4096 || json.Unmarshal(result, &out) != nil || out.ID < 1 {
		return ProposalReceipt{}, invalidResponse
	}
	if _, err := time.Parse(time.RFC3339, out.ExpiresAt); err != nil {
		return ProposalReceipt{}, invalidResponse
	}
	switch out.Status {
	case "pending", "approved", "rejected", "expired", "revoked":
		return out, nil
	default:
		return ProposalReceipt{}, invalidResponse
	}
}
