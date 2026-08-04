package dataintegration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrInvalidToolSchema = errors.New("invalid tool schema")

// ToolSchemaImportResult describes a non-destructive registry merge.
type ToolSchemaImportResult struct {
	AddedTools        int `json:"added_tools"`
	AddedVersions     int `json:"added_versions"`
	UpdatedVersions   int `json:"updated_versions"`
	SkippedIncomplete int `json:"skipped_incomplete"`
	SkippedInvalid    int `json:"skipped_invalid"`
	TotalTools        int `json:"total_tools"`
	TotalVersions     int `json:"total_versions"`
}

// ToolSchemaEditResult describes one definition saved as a registry version.
type ToolSchemaEditResult struct {
	Name          string `json:"name"`
	SchemaHash    string `json:"schema_hash"`
	Added         bool   `json:"added"`
	TotalTools    int    `json:"total_tools"`
	TotalVersions int    `json:"total_versions"`
}

// ToolSchemaClearResult describes a destructive registry reset.
type ToolSchemaClearResult struct {
	RemovedTools    int `json:"removed_tools"`
	RemovedVersions int `json:"removed_versions"`
}

// ExportToolSchemas returns the complete persisted registry document.
func (s *Store) ExportToolSchemas() ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("data integration store is unavailable")
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return nil, errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return nil, errInit
	}
	return json.MarshalIndent(s.schemaTable.snapshot(), "", "  ")
}

// ClearToolSchemas removes every stored schema without touching sessions.
func (s *Store) ClearToolSchemas() (ToolSchemaClearResult, error) {
	if s == nil {
		return ToolSchemaClearResult{}, fmt.Errorf("data integration store is unavailable")
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return ToolSchemaClearResult{}, errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return ToolSchemaClearResult{}, errInit
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	tools, versions := s.schemaTable.clearAll()
	if errWrite := s.writeToolSchemas(); errWrite != nil {
		return ToolSchemaClearResult{}, errWrite
	}
	return ToolSchemaClearResult{RemovedTools: tools, RemovedVersions: versions}, nil
}

// ImportToolSchemas merges every valid signature without replacing original fields.
func (s *Store) ImportToolSchemas(payload []byte) (ToolSchemaImportResult, error) {
	if s == nil {
		return ToolSchemaImportResult{}, fmt.Errorf("data integration store is unavailable")
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return ToolSchemaImportResult{}, errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return ToolSchemaImportResult{}, errInit
	}
	var imported persistedToolSchemaTable
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&imported); errDecode != nil {
		return ToolSchemaImportResult{}, fmt.Errorf("%w: decode registry: %v", ErrInvalidToolSchema, errDecode)
	}
	if imported.Version != toolSchemaTableVersion {
		return ToolSchemaImportResult{}, fmt.Errorf(
			"%w: unsupported registry version %d",
			ErrInvalidToolSchema,
			imported.Version,
		)
	}
	result := s.schemaTable.mergeDefinitions(imported)
	if result.AddedVersions > 0 || result.UpdatedVersions > 0 {
		if errWrite := s.writeToolSchemas(); errWrite != nil {
			return ToolSchemaImportResult{}, errWrite
		}
	}
	result.TotalTools, result.TotalVersions = s.schemaTable.counts()
	return result, nil
}

// PutToolSchema adds one complete definition as a new version.
func (s *Store) PutToolSchema(name string, definition json.RawMessage) (ToolSchemaEditResult, error) {
	if s == nil {
		return ToolSchemaEditResult{}, fmt.Errorf("data integration store is unavailable")
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return ToolSchemaEditResult{}, errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return ToolSchemaEditResult{}, errInit
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolSchemaEditResult{}, fmt.Errorf("%w: tool name is required", ErrInvalidToolSchema)
	}
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(definition))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&decoded); errDecode != nil || decoded == nil {
		return ToolSchemaEditResult{}, fmt.Errorf("%w: definition must be a JSON object", ErrInvalidToolSchema)
	}
	definitionName := firstString(decoded, "name")
	if definitionName == "" {
		definitionName = lowerString(decoded["type"])
	}
	if definitionName != name {
		return ToolSchemaEditResult{}, fmt.Errorf(
			"%w: definition name %q does not match %q",
			ErrInvalidToolSchema,
			definitionName,
			name,
		)
	}
	if !completeRawToolDefinition(decoded) {
		return ToolSchemaEditResult{}, fmt.Errorf(
			"%w: definition requires a meaningful name, description, and complete object parameter schema",
			ErrInvalidToolSchema,
		)
	}
	encoded, hash, errEncode := encodeToolDefinition(decoded)
	if errEncode != nil {
		return ToolSchemaEditResult{}, fmt.Errorf("%w: encode definition: %v", ErrInvalidToolSchema, errEncode)
	}
	schema, _ := firstMap(decoded, "parameters", "input_schema", "parametersJsonSchema")
	added := s.schemaTable.putComplete(name, hash, encoded, schema, time.Now().UTC())
	if added {
		if errWrite := s.writeToolSchemas(); errWrite != nil {
			return ToolSchemaEditResult{}, errWrite
		}
	}
	toolCount, versionCount := s.schemaTable.counts()
	return ToolSchemaEditResult{
		Name:          name,
		SchemaHash:    hash,
		Added:         added,
		TotalTools:    toolCount,
		TotalVersions: versionCount,
	}, nil
}

func (s *Store) writeToolSchemas() error {
	s.schemaWriteMu.Lock()
	defer s.schemaWriteMu.Unlock()
	return s.schemaTable.write(s.schemaPath)
}

func (t *toolSchemaTable) snapshot() persistedToolSchemaTable {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.persistedLocked(time.Now().UTC())
}

func (t *toolSchemaTable) persistedLocked(updatedAt time.Time) persistedToolSchemaTable {
	stored := persistedToolSchemaTable{
		Version:   toolSchemaTableVersion,
		UpdatedAt: updatedAt,
		Tools:     make(map[string]persistedToolSchemaSet, len(t.tools)),
	}
	for name, versions := range t.tools {
		hashes := make([]string, 0, len(versions))
		for hash := range versions {
			hashes = append(hashes, hash)
		}
		sort.Strings(hashes)
		set := persistedToolSchemaSet{
			Versions: make([]persistedToolSchemaVersion, 0, len(hashes)),
		}
		for _, hash := range hashes {
			version := versions[hash]
			set.Versions = append(set.Versions, persistedToolSchemaVersion{
				SchemaHash:             version.hash,
				Definition:             append(json.RawMessage(nil), version.definition...),
				ContractSchemaComplete: version.complete,
				ObservedCount:          version.observed,
				FirstSeen:              version.firstSeen,
				LastSeen:               version.lastSeen,
			})
		}
		stored.Tools[name] = set
	}
	return stored
}

func (t *toolSchemaTable) mergeDefinitions(imported persistedToolSchemaTable) ToolSchemaImportResult {
	result := ToolSchemaImportResult{}
	t.mu.Lock()
	defer t.mu.Unlock()
	beforeTools := len(t.tools)
	beforeVersions := 0
	for _, versions := range t.tools {
		beforeVersions += len(versions)
	}
	touched := make(map[string]struct{})
	for name, set := range imported.Tools {
		name = strings.TrimSpace(name)
		for _, candidate := range set.Versions {
			var definition map[string]any
			decoder := json.NewDecoder(bytes.NewReader(candidate.Definition))
			decoder.UseNumber()
			if name == "" || decoder.Decode(&definition) != nil || definition == nil {
				result.SkippedInvalid++
				continue
			}
			definitionName := firstString(definition, "name")
			if definitionName == "" {
				definitionName = lowerString(definition["type"])
			}
			if definitionName != name {
				result.SkippedInvalid++
				continue
			}
			schema, hasSchema := firstMap(definition, "parameters", "input_schema", "parametersJsonSchema")
			if !hasSchema {
				result.SkippedInvalid++
				continue
			}
			encoded, hash, errEncode := encodeToolDefinition(definition)
			if errEncode != nil {
				result.SkippedInvalid++
				continue
			}
			versions := t.tools[name]
			if versions == nil {
				versions = make(map[string]*toolSchemaVersion)
				t.tools[name] = versions
			}
			existing := versions[hash]
			if existing == nil {
				observed := candidate.ObservedCount
				if observed == 0 {
					observed = 1
				}
				versions[hash] = &toolSchemaVersion{
					hash:       hash,
					definition: encoded,
					schema:     schema,
					complete:   completeRawToolDefinition(definition),
					observed:   observed,
					firstSeen:  candidate.FirstSeen,
					lastSeen:   candidate.LastSeen,
				}
				t.dirty = true
				touched[name] = struct{}{}
				continue
			}
			changed := false
			if candidate.ObservedCount > existing.observed {
				existing.observed = candidate.ObservedCount
				changed = true
			}
			if earlierNonZero(candidate.FirstSeen, existing.firstSeen) {
				existing.firstSeen = candidate.FirstSeen
				changed = true
			}
			if candidate.LastSeen.After(existing.lastSeen) {
				existing.lastSeen = candidate.LastSeen
				changed = true
			}
			if changed {
				result.UpdatedVersions++
				t.dirty = true
			}
			touched[name] = struct{}{}
		}
	}
	if repaired := t.repairDescriptionsLocked(touched); repaired > 0 {
		result.UpdatedVersions += repaired
	}
	if t.compactLocked() > 0 {
		t.dirty = true
	}
	afterVersions := 0
	for _, versions := range t.tools {
		afterVersions += len(versions)
	}
	if len(t.tools) > beforeTools {
		result.AddedTools = len(t.tools) - beforeTools
	}
	if afterVersions > beforeVersions {
		result.AddedVersions = afterVersions - beforeVersions
	}
	return result
}

func (t *toolSchemaTable) putComplete(
	name, hash string,
	definition json.RawMessage,
	schema map[string]any,
	observedAt time.Time,
) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	versions := t.tools[name]
	if versions == nil {
		versions = make(map[string]*toolSchemaVersion)
		t.tools[name] = versions
	}
	if versions[hash] != nil {
		return false
	}
	versions[hash] = &toolSchemaVersion{
		hash:       hash,
		definition: append(json.RawMessage(nil), definition...),
		schema:     schema,
		complete:   true,
		observed:   1,
		firstSeen:  observedAt,
		lastSeen:   observedAt,
	}
	t.dirty = true
	return true
}

func earlierNonZero(candidate, current time.Time) bool {
	return !candidate.IsZero() && (current.IsZero() || candidate.Before(current))
}
