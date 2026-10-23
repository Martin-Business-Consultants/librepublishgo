package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Bundle is a rate book as Fuse's `bin/rails pricing:export` writes it
// (fuse/lib/tasks/pricing_export.rake).
type Bundle struct {
	Format            string          `json:"format"` // "fuse-rates"
	Version           int             `json:"version"`
	Tenant            string          `json:"tenant"`
	ExportedAt        string          `json:"exported_at"`
	FuseSchemaVersion json.Number     `json:"fuse_schema_version"`
	SHA256            string          `json:"sha256"`
	Rates             json.RawMessage `json:"rates"`
	// DistanceFixture, when present, is recorded Directions answers by
	// route (RouteKey.String()), for the plugin's fixture distance mode.
	DistanceFixture map[string]FixtureRoute `json:"distance_fixture,omitempty"`
}

// FixtureRoute is one recorded Directions answer: OK with its legs, or the
// status Google gave instead (ZERO_RESULTS).
type FixtureRoute struct {
	Status      string  `json:"status"`
	LegsMeters  []int64 `json:"legs_meters,omitempty"`
	LegsSeconds []int64 `json:"legs_seconds,omitempty"`
}

// ParseBundle reads a Fuse export and its book, and checks it's whole: the
// format, and the SHA-256 Fuse wrote of the rates' exact bytes.
func ParseBundle(data []byte) (*Bundle, *Book, error) {
	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&b); err != nil {
		return nil, nil, fmt.Errorf("not a Fuse rate export: %w", err)
	}
	if b.Format != "fuse-rates" {
		return nil, nil, errors.New(`not a Fuse rate export (its "format" isn't "fuse-rates")`)
	}
	if b.Version != 1 {
		return nil, nil, fmt.Errorf("a version %d export; this plugin reads version 1", b.Version)
	}
	if len(b.Rates) == 0 {
		return nil, nil, errors.New("the export has no rates")
	}
	sum := sha256.Sum256(b.Rates)
	if b.SHA256 == "" || hex.EncodeToString(sum[:]) != b.SHA256 {
		return nil, nil, errors.New("the export's rates don't match its sha256: it was edited or cut short since Fuse wrote it")
	}
	var book Book
	if err := json.Unmarshal(b.Rates, &book); err != nil {
		return nil, nil, fmt.Errorf("the export's rates don't read: %w", err)
	}
	return &b, &book, nil
}
