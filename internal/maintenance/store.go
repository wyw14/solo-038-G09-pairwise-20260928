package maintenance

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// recordType discriminators for the append-only maintenance journal.
const (
	recordUsage   = "usage"
	recordService = "service"
)

// storedRecord is the on-disk envelope for one journal line. Exactly one
// field is populated per line.
type storedRecord struct {
	Type    string         `json:"type"`
	Usage   *UsageRecord   `json:"usage,omitempty"`
	Service *ServiceRecord `json:"service,omitempty"`
}

// Store appends maintenance records to a JSONL journal. Records are only
// ever appended; nothing is rewritten or deleted, so historical overdue
// crossings survive every later edit. Overdue latches are not journaled
// separately: they are projected from this append-only timeline, which
// makes the journal crash-consistent with no two-phase writes.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

// Append writes one record as a single fsynced journal line.
func (store *Store) Append(record storedRecord) error {
	if record.Type == "" {
		return fmt.Errorf("maintenance record type is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(store.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	if err = encoder.Encode(record); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("append maintenance record: %w", err)
	}
	return nil
}

// Load replays every journaled record in append order.
func (store *Store) Load() ([]storedRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	records := []storedRecord{}
	for scanner.Scan() {
		var record storedRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode maintenance record: %w", err)
		}
		switch record.Type {
		case recordUsage:
			if record.Usage == nil {
				return nil, fmt.Errorf("maintenance usage record is empty")
			}
		case recordService:
			if record.Service == nil {
				return nil, fmt.Errorf("maintenance service record is empty")
			}
		default:
			return nil, fmt.Errorf("maintenance record type %q is unknown", record.Type)
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}
