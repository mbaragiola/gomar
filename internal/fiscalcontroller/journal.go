package fiscalcontroller

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const journalVersion = 1

type savedRecord struct {
	Request Request
	Result  Result
}

type journalState struct {
	Version           int
	DeviceID          string
	SimulatorIdentity string
	Config            Config
	Pending           string
	Records           map[string]savedRecord
}

type fileJournal struct {
	simulatorIdentity string
	path              string
	deviceID          string
}

// Open handles are exclusive within this process only. Independent processes
// require a durable single-writer mechanism that the production app enforces.
var openJournals = struct {
	sync.Mutex
	paths map[string]bool
}{paths: make(map[string]bool)}

// CreateSimulatorJournal creates a new journal and never overwrites one.
// Production devices must not use this reference file format.
func CreateSimulatorJournal(path, deviceID string, s *Simulator, cfg Config) (*Controller, error) {
	return openSimulatorJournal(path, deviceID, s, cfg, true)
}

// OpenSimulatorJournal resumes an existing journal without performing I/O on
// the device. The caller must retain the same simulated device independently
// of the journal, as it would retain the physical device across a restart.
func OpenSimulatorJournal(path, deviceID string, s *Simulator, cfg Config) (*Controller, error) {
	return openSimulatorJournal(path, deviceID, s, cfg, false)
}

func openSimulatorJournal(path, deviceID string, s *Simulator, cfg Config, create bool) (*Controller, error) {
	if s == nil || deviceID == "" || path == "" || s.pointOfSale != cfg.PointOfSale {
		return nil, ErrInvalid
	}
	c, err := New(s, cfg)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid path: %w", ErrJournal, err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid directory: %w", ErrJournal, err)
	}
	j := &fileJournal{path: filepath.Join(dir, filepath.Base(abs)), deviceID: deviceID, simulatorIdentity: s.identity}
	if !j.reserve() {
		return nil, fmt.Errorf("%w: already open", ErrJournal)
	}
	c.journal = j
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journalPath != "" && (create || s.journalPath != j.path) {
		j.release()
		return nil, fmt.Errorf("%w: simulator is already bound to a journal", ErrJournal)
	}
	if create {
		err = j.save(c.snapshot(), true)
	} else {
		var state journalState
		state, err = j.load()
		if err == nil {
			err = c.restore(state)
		}
	}
	if err != nil {
		j.release()
		return nil, fmt.Errorf("%w: %w", ErrJournal, err)
	}
	s.journalPath = j.path
	return c, nil
}

func (j *fileJournal) reserve() bool {
	openJournals.Lock()
	defer openJournals.Unlock()
	if openJournals.paths[j.path] {
		return false
	}
	openJournals.paths[j.path] = true
	return true
}
func (j *fileJournal) release() {
	openJournals.Lock()
	defer openJournals.Unlock()
	delete(openJournals.paths, j.path)
}

// Close waits for current work, then permits reopening the same journal in
// this process. Closed controllers cannot issue or reconcile.
func (c *Controller) Close() {
	c.gate <- struct{}{}
	defer c.unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.journal != nil {
		c.journal.release()
	}
}

func (c *Controller) snapshot() journalState {
	rows := make(map[string]savedRecord, len(c.records))
	for k, v := range c.records {
		rows[k] = savedRecord{v.request, v.result}
	}
	return journalState{Version: journalVersion, DeviceID: c.journal.deviceID, SimulatorIdentity: c.journal.simulatorIdentity, Config: c.config, Pending: c.pending, Records: rows}
}

func (c *Controller) restore(state journalState) error {
	if state.Version != journalVersion || state.DeviceID != c.journal.deviceID || state.SimulatorIdentity != c.journal.simulatorIdentity || state.Config != c.config || state.Records == nil {
		return errors.New("journal format, identity or device configuration does not match")
	}
	pending := ""
	for k, v := range state.Records {
		if k == "" || k != v.Request.SaleID || v.Request.TotalCentavos <= 0 || !c.config.Capabilities.supports(v.Request.Kind) {
			return errors.New("invalid journal request")
		}
		switch v.Result.State {
		case OutcomeUnknown:
			if pending != "" || v.Result.Document != (Document{}) {
				return errors.New("invalid unknown outcome")
			}
			pending = k
		case ConfirmedNotIssued:
			if v.Result.Document != (Document{}) {
				return errors.New("invalid not-issued outcome")
			}
		case Issued:
			doc := v.Result.Document
			if !c.valid(v.Request, doc) {
				return errors.New("invalid stored fiscal document")
			}
			c.issuedNumbers[documentNumber{doc.Kind, doc.Number}] = k
		default:
			return errors.New("invalid journal state")
		}
		c.records[k] = record{v.Request, v.Result}
	}
	if state.Pending != pending {
		return errors.New("invalid pending pointer")
	}
	c.pending = pending
	return nil
}

func (j *fileJournal) load() (journalState, error) {
	var state journalState
	info, err := os.Lstat(j.path)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return state, errors.New("journal must be a private regular file")
	}
	f, err := os.Open(j.path)
	if err != nil {
		return state, err
	}
	defer f.Close()
	var stored journalEnvelope
	if err := decodeOne(f, &stored); err != nil {
		return state, err
	}
	if stored.Digest != sha256.Sum256(stored.Payload) {
		return state, errors.New("journal checksum does not match")
	}
	if err := decodeOne(bytes.NewReader(stored.Payload), &state); err != nil {
		return state, err
	}
	return state, nil
}

// A checksum detects accidental corruption, not user tampering or rollback.
type journalEnvelope struct {
	Payload json.RawMessage
	Digest  [sha256.Size]byte
}

func decodeOne(r io.Reader, dest any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing journal content")
	}
	return nil
}

func (j *fileJournal) save(state journalState, create bool) error {
	dir := filepath.Dir(j.path)
	if !create {
		if st, err := os.Lstat(j.path); err != nil {
			return err
		} else if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return errors.New("journal must be a private regular file")
		}
	}
	tmp, err := os.CreateTemp(dir, ".fiscal-journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	payload, encodeErr := json.Marshal(state)
	if encodeErr == nil {
		encodeErr = json.NewEncoder(tmp).Encode(journalEnvelope{Payload: payload, Digest: sha256.Sum256(payload)})
	}
	if encodeErr == nil {
		encodeErr = tmp.Sync()
	}
	if err := errors.Join(encodeErr, tmp.Close()); err != nil {
		return err
	}
	if create {
		if err := os.Link(tmp.Name(), j.path); err != nil {
			return err
		}
	} else if err := os.Rename(tmp.Name(), j.path); err != nil {
		return err
	}
	fd, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer fd.Close()
	return fd.Sync()
}
