package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	stagedIntakeTTL = 24 * time.Hour
	intakeDraft     = "manage_draft_documents"
	intakeDownload  = "download_attachments"
)

var (
	missingIntake     = problem{404, "No se encontró la carga temporal."}
	missingIntakeFile = problem{404, "No se encontró el archivo temporal."}
	expiredIntake     = problem{410, "La carga temporal venció. Volvé a subir el archivo."}
	closedIntake      = problem{409, "Esta carga ya se está procesando o ya fue cerrada."}
)

type intakeRecord struct {
	id, status, mime, digest, storage string
	size, expires                     int64
	entityID, outletID, ownerID       int64
}

type intakeReceipt struct {
	ID     string `json:"id"`
	Status string `json:"estado"`
	MIME   string `json:"tipo"`
	Size   int64  `json:"tamano"`
	Pages  int    `json:"paginas"`
	Expiry string `json:"vence"`
}

func newIntakeID() string {
	var raw [16]byte
	rand.Read(raw[:])
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	v := hex.EncodeToString(raw[:])
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:]
}

func intakeUUID(input string) string {
	if len(input) != 36 {
		return ""
	}
	id := strings.ToLower(input)
	if len(id) != len(input) {
		return ""
	}
	for i := range len(id) {
		b := id[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return ""
			}
		} else if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return ""
		}
	}
	return id
}

func intakeStorage(id string) string { return "intake-" + id }

func (a *app) intakeScope(r *http.Request, u user, capability string) (*sql.Tx, entityMembership, error) {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return nil, entityMembership{}, err
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, entityMembership{}, err
	}
	// Recheck the request's session/account in the same write transaction
	// as the membership and active outlet grant.
	current, err := currentUser(nil, r, tx)
	if err == nil && (current.id == 0 || current.id != u.id) {
		err = unauthenticated
	}
	if err != nil {
		tx.Rollback()
		return nil, entityMembership{}, err
	}
	m, err := scopedMembership(tx, current.id, entityID, capability)
	if err != nil {
		tx.Rollback()
		return nil, entityMembership{}, err
	}
	if err := a.lockDocumentWrite(r, tx, u, entityID); err != nil {
		tx.Rollback()
		return nil, entityMembership{}, err
	}
	m, err = scopedMembership(tx, u.id, entityID, capability)
	if err != nil {
		tx.Rollback()
		return nil, entityMembership{}, err
	}
	return tx, m, nil
}

func authorizedIntakeOutlet(tx *sql.Tx, m entityMembership, outletID int64) error {
	var found bool
	err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM outlets o
		JOIN outlet_grants g ON g.outlet_id=o.id
		WHERE o.id=$1 AND o.active AND o.entity_id=$2 AND g.membership_id=$3)`,
		outletID, m.ID, m.id).Scan(&found)
	if err == nil && !found {
		err = missingOutlet
	}
	return err
}

func (a *app) checkIntakeUpload(r *http.Request, u user, outletID int64) error {
	tx, m, err := a.intakeScope(r, u, intakeDraft)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return authorizedIntakeOutlet(tx, m, outletID)
}

func syncIntakeDirectory(root *os.Root) error {
	folder, err := root.Open(".")
	if err != nil {
		return err
	}
	defer folder.Close()
	return folder.Sync()
}

func (a *app) uploadStagedIntake(w http.ResponseWriter, r *http.Request, u user) error {
	w.Header().Set("Cache-Control", "private, no-store")
	outletID, err := pathID(r, "outlet")
	if err != nil {
		return err
	}
	if err := a.checkIntakeUpload(r, u, outletID); err != nil {
		return err
	}
	// Read, bound and validate browser upload bytes outside the database lock.
	meta, data, err := browserPrivateImage(w, r, "Las cargas temporales PDF todavía no están disponibles en esta versión.")
	if err != nil {
		return err
	}
	tx, m, err := a.intakeScope(r, u, intakeDraft)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := authorizedIntakeOutlet(tx, m, outletID); err != nil {
		return err
	}
	if a.privateRoot == nil {
		return errors.New("private intake storage not configured")
	}
	id := newIntakeID()
	name := intakeStorage(id)
	file, err := a.privateRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if err := a.privateRoot.Remove(name); err != nil {
				log.Print("could not remove uncommitted staged image")
			}
		}
	}()
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := syncIntakeDirectory(a.privateRoot); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(stagedIntakeTTL)
	digest := sha256.Sum256(data)
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO intake_operations
		(id,entity_id,outlet_id,user_id,status,mime_type,size,digest,storage_name,created_at,expires_at)
		VALUES ($1,$2,$3,$4,'uploaded',$5,$6,$7,$8,$9,$10)`,
		id, m.ID, outletID, u.id, meta.MIME, meta.Size, hex.EncodeToString(digest[:]), name, now.UnixMicro(), expires.UnixMicro()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		if a.retainAfterDocumentCommitError(err) {
			committed = true // unknown outcome; do not unlink a possible reference
			log.Print("private image retained after an uncertain database commit")
		}
		return err
	}
	committed = true
	jsonResponse(w, 201, intakeReceipt{id, "uploaded", meta.MIME, meta.Size, 1, expires.Format("2006-01-02T15:04:05.000000-07:00")})
	return nil
}

func scopedIntakeRecord(tx *sql.Tx, m entityMembership, id string) (intakeRecord, error) {
	record := intakeRecord{id: id}
	if id == "" {
		return record, missingIntake
	}
	err := tx.QueryRow(`SELECT i.status,i.mime_type,i.digest,i.size,i.expires_at,coalesce(i.storage_name,''),i.entity_id,i.outlet_id,i.user_id
		FROM intake_operations i
		JOIN outlets o ON o.id=i.outlet_id AND o.entity_id=i.entity_id AND o.active
		JOIN outlet_grants g ON g.outlet_id=i.outlet_id AND g.membership_id=$1
		WHERE i.id=$2 AND i.entity_id=$3 AND i.user_id=$4`, m.id, id, m.ID, m.userID).
		Scan(&record.status, &record.mime, &record.digest, &record.size, &record.expires, &record.storage, &record.entityID, &record.outletID, &record.ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		err = missingIntake
	}
	return record, err
}

func (op intakeRecord) live() bool {
	switch op.status {
	case "uploaded", "failed", "ready", "processing":
		return true
	default:
		return false
	}
}

func (a *app) downloadStagedIntake(w http.ResponseWriter, r *http.Request, u user) error {
	return a.readOrCancelStagedIntake(w, r, u, false)
}

func (a *app) cancelStagedIntake(w http.ResponseWriter, r *http.Request, u user) error {
	return a.readOrCancelStagedIntake(w, r, u, true)
}

func (a *app) readOrCancelStagedIntake(w http.ResponseWriter, r *http.Request, u user, cancel bool) error {
	w.Header().Set("Cache-Control", "private, no-store")
	capability := intakeDownload
	if cancel {
		capability = intakeDraft
	}
	tx, m, err := a.intakeScope(r, u, capability)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := scopedIntakeRecord(tx, m, intakeUUID(r.PathValue("operation")))
	if err != nil {
		return err
	}
	var outcome error
	var terminal string
	switch {
	case !op.live():
		outcome = missingIntakeFile
		if cancel {
			outcome = closedIntake
		}
	case op.expires <= time.Now().UnixMicro():
		terminal, outcome = "expired", expiredIntake
	case cancel && op.status == "processing":
		return closedIntake
	case cancel:
		terminal = "cancelled"
	}
	if terminal != "" || outcome != nil {
		if err := a.finishStagedIntake(r.Context(), tx, op, terminal); err != nil {
			return err
		}
		if outcome != nil {
			return outcome
		}
		jsonResponse(w, 200, map[string]string{"detalle": "Carga temporal descartada."})
		return nil
	}
	data, err := a.readStagedBytes(op)
	if err != nil {
		return missingIntakeFile
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	name := "comprobante.png"
	if op.mime == "image/jpeg" {
		name = "comprobante.jpg"
	}
	w.Header().Set("Content-Type", op.mime)
	w.Header().Set("Content-Length", strconv.FormatInt(op.size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(data); err != nil {
		log.Print("incomplete staged image download")
	}
	return nil
}

// finishStagedIntake commits a terminal transition before attempting file
// removal. Previously closed operations may retry an interrupted cleanup.
func (a *app) finishStagedIntake(ctx context.Context, tx *sql.Tx, op intakeRecord, terminal string) error {
	if terminal != "" {
		res, err := tx.ExecContext(ctx, `UPDATE intake_operations SET status=$1
			WHERE id=$2 AND status=$3`, terminal, op.id, op.status)
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("staged image transition lost its claim")
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if op.storage != "" {
		if err := a.cleanupTerminalIntake(ctx, op.id); err != nil {
			// Keep the tracked reference; authorized retries can finish cleanup.
			log.Print("could not finish staged image cleanup")
		}
	}
	return nil
}

func (a *app) readStagedBytes(op intakeRecord) ([]byte, error) {
	if a.privateRoot == nil || intakeUUID(op.id) != op.id || op.id == "" ||
		op.storage != intakeStorage(op.id) || op.size < 1 || op.size > maxReceiptBytes ||
		(op.mime != "image/png" && op.mime != "image/jpeg") {
		return nil, errors.New("invalid staged image reference")
	}
	f, err := a.privateRoot.OpenFile(op.storage, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, _ := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != op.size || stat == nil || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("unsupported staged image file")
	}
	data, err := io.ReadAll(io.LimitReader(f, op.size+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != op.size || hex.EncodeToString(sum[:]) != op.digest {
		return nil, errors.New("staged image content differs from its reference")
	}
	return data, nil
}

func (a *app) cleanupTerminalIntake(ctx context.Context, id string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op := intakeRecord{id: id}
	query := `SELECT mime_type,digest,size,storage_name FROM intake_operations
		WHERE id=$1 AND status IN ('cancelled','expired','confirmed') AND storage_name IS NOT NULL FOR UPDATE`
	err = tx.QueryRowContext(ctx, query, id).
		Scan(&op.mime, &op.digest, &op.size, &op.storage)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := a.readStagedBytes(op); err == nil {
		if err := a.privateRoot.Remove(op.storage); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncIntakeDirectory(a.privateRoot); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE intake_operations SET storage_name=NULL
		WHERE id=$1 AND status IN ('cancelled','expired','confirmed') AND storage_name=$2`, id, op.storage); err != nil {
		return err
	}
	return tx.Commit()
}
