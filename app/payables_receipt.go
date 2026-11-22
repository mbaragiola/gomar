package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const maxReceiptBytes = 10 << 20
const maxReceiptBody = 11 << 20

var (
	duplicateReceipt = problem{409, "El pago ya tiene un comprobante adjunto."}
	noReceipt        = problem{404, "No se encontró el comprobante del pago."}
	noReceiptFile    = problem{404, "No se encontró el archivo del comprobante."}
	receiptTooLarge  = problem{422, "El archivo supera el límite de 10 MB."}
)

type receiptMetadata struct {
	Name string `json:"nombre"`
	MIME string `json:"tipo"`
	Size int64  `json:"tamano"`
}

func browserReceipt(w http.ResponseWriter, r *http.Request) (receiptMetadata, []byte, error) {
	return browserPrivateImage(w, r, "Los comprobantes PDF todavía no están disponibles en esta versión.")
}

func browserPrivateImage(w http.ResponseWriter, r *http.Request, pdfUnavailable string) (receiptMetadata, []byte, error) {
	var metadata receiptMetadata
	kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	boundary := params["boundary"]
	if err != nil || kind != "multipart/form-data" || len(boundary) == 0 || len(boundary) > 70 ||
		strings.ContainsAny(boundary, "\r\n") {
		return metadata, nil, badInput
	}
	wire, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReceiptBody))
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			return metadata, nil, receiptTooLarge
		}
		return metadata, nil, badInput
	}
	// This local slice accepts the standard one-file browser shape only. Bound
	// its raw part headers separately from the body and reject encoded parts.
	prefix := []byte("--" + boundary + "\r\n")
	headers, ok := bytes.CutPrefix(wire, prefix)
	end := bytes.Index(headers[:min(len(headers), 1024)], []byte("\r\n\r\n"))
	if !ok || end < 0 {
		return metadata, nil, badInput
	}
	reader := multipart.NewReader(bytes.NewReader(wire), boundary)
	part, err := reader.NextRawPart()
	if err != nil || part.FormName() != "archivo" || part.FileName() == "" ||
		len(part.Header.Values("Content-Transfer-Encoding")) != 0 {
		return metadata, nil, badInput
	}
	defer part.Close()
	name := part.FileName()
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return metadata, nil, badInput
	}
	extension := strings.ToLower(path.Ext(strings.TrimLeft(name, ".")))
	payload, err := io.ReadAll(io.LimitReader(part, maxReceiptBytes+1))
	if err != nil {
		return metadata, nil, badInput
	}
	if len(payload) > maxReceiptBytes {
		return metadata, nil, receiptTooLarge
	}
	if _, err := reader.NextRawPart(); !errors.Is(err, io.EOF) {
		return metadata, nil, badInput
	}
	var expected, expectedFormat string
	var signature []byte
	switch extension {
	case ".png":
		expected, expectedFormat, signature = "image/png", "png", []byte("\x89PNG\r\n\x1a\n")
	case ".jpg", ".jpeg":
		expected, expectedFormat, signature = "image/jpeg", "jpeg", []byte("\xff\xd8\xff")
	case ".pdf":
		return metadata, nil, problem{422, pdfUnavailable}
	default:
		return metadata, nil, problem{422, "Solo se permiten archivos PDF, PNG y JPEG."}
	}
	contentType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || contentType != expected || !bytes.HasPrefix(payload, signature) {
		return metadata, nil, problem{422, "El contenido del archivo no coincide con su tipo."}
	}
	damaged := problem{422, "La imagen está dañada o no es válida."}
	dimensions, format, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil || format != expectedFormat || dimensions.Width <= 0 || dimensions.Height <= 0 {
		return metadata, nil, damaged
	}
	if dimensions.Width > 40_000_000/dimensions.Height {
		return metadata, nil, problem{422, "La imagen tiene demasiados píxeles."}
	}
	if _, _, err := image.Decode(bytes.NewReader(payload)); err != nil {
		return metadata, nil, damaged
	}
	runes := []rune(name)
	metadata = receiptMetadata{string(runes[:min(len(runes), 255)]), expected, int64(len(payload))}
	return metadata, payload, nil
}

func visibleReceiptPayment(r *http.Request, tx *sql.Tx, u user) (entityMembership, paymentRecord, error) {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return entityMembership{}, paymentRecord{}, err
	}
	m, err := scopedMembership(tx, u.id, entityID, recordPayments)
	if err != nil {
		return m, paymentRecord{}, err
	}
	if err := requireFinancialOutlet(tx, m); err != nil {
		return m, paymentRecord{}, err
	}
	payment, err := paymentFromPath(r, tx, m)
	return m, payment, err
}

func (a *app) checkReceiptUpload(r *http.Request, u user) error {
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, payment, err := visibleReceiptPayment(r, tx, u)
	if err == nil && payment.HasReceipt {
		return duplicateReceipt
	}
	return err
}

func (a *app) attachReceipt(w http.ResponseWriter, r *http.Request, u user) error {
	if err := a.checkReceiptUpload(r, u); err != nil {
		return err
	}
	metadata, payload, err := browserReceipt(w, r)
	if err != nil {
		return err
	}
	// Neither reading a request body nor parsing a file holds the writer lock.
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, payment, err := visibleReceiptPayment(r, tx, u)
	if err != nil {
		return err
	}
	if err := lockPostgresEntity(r.Context(), tx, m.ID); err != nil {
		return err
	}
	// A competing attachment, reversal, or outlet-scope change can have
	// committed while this request was waiting for the entity lock.
	m, payment, err = visibleReceiptPayment(r, tx, u)
	if err != nil {
		return err
	}
	if payment.HasReceipt {
		return duplicateReceipt
	}
	if a.privateRoot == nil {
		return errors.New("private receipt storage not configured")
	}
	var random [16]byte
	rand.Read(random[:])
	name := hex.EncodeToString(random[:])
	file, err := a.privateRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if err := a.privateRoot.Remove(name); err != nil {
				log.Print("could not remove uncommitted private receipt")
			}
		}
	}()
	_, writeErr := file.Write(payload)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	at := formatPayableStorageTime(a.payableNow())
	_, err = tx.Exec(`INSERT INTO ap_receipts(payment_id,storage_name,original_filename,mime_type,size,uploaded_by_id,uploaded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, payment.ID, name, metadata.Name, metadata.MIME, metadata.Size, u.id, at)
	if err != nil {
		return err
	}
	if err := appendPayablesEvent(tx, m, nil, &payment.ID, "attach_receipt", "Comprobante: "+metadata.Name, at,
		map[string]any{"mime_type": metadata.MIME, "size": metadata.Size}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		// A PostgreSQL connection failure during COMMIT can leave its outcome
		// unknown. Keep the private file rather than deleting a possibly
		// committed reference; a later controlled cleanup can verify it.
		committed = true
		log.Print("preserving private receipt after uncertain commit")
		return err
	}
	committed = true
	jsonResponse(w, 201, metadata)
	return nil
}

func (a *app) downloadReceipt(w http.ResponseWriter, r *http.Request, u user) error {
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, payment, err := visibleReceiptPayment(r, tx, u)
	if err != nil {
		return err
	}
	var name string
	var metadata receiptMetadata
	err = tx.QueryRow(`SELECT storage_name,original_filename,mime_type,size FROM ap_receipts WHERE payment_id=$1`,
		payment.ID).Scan(&name, &metadata.Name, &metadata.MIME, &metadata.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return noReceipt
	}
	if err != nil {
		return err
	}
	if a.privateRoot == nil {
		return noReceiptFile
	}
	file, err := a.privateRoot.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return noReceiptFile
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != metadata.Size {
		return noReceiptFile
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": metadata.Name})
	if strings.IndexFunc(metadata.Name, func(char rune) bool { return char > 127 }) < 0 {
		disposition = fmt.Sprintf("attachment; filename=%q", metadata.Name)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", metadata.MIME)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.Size, 10))
	io.CopyN(w, file, metadata.Size)
	return nil
}
