// Package wisdev — MCP runtime configuration ("tuning") surface.
//
// This file makes the WisDev ARC MCP runtime fully tunable by an external LLM.
// Every knob that influences search, evidence, author, and manuscript behavior
// is declared once in knobRegistry() with a type, range/enum, default, and
// human-readable description. The MCP tools wisdevGetConfig / wisdevTuneConfig /
// wisdevResetConfig let an external agent discover the full tunable surface,
// read current values, and change them; the action tools (mcp_server.go) read
// these values as their per-call defaults so tuning actually takes effect.
//
// The configuration is a single declared key→value space guarded by a mutex.
// "Tune anything" is implemented generically: tuning validates each update
// against its knob descriptor (kind, bounds, enum) rather than hardcoding a
// setter per field, so new knobs only need a knobRegistry() entry.
package wisdev

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// configKind enumerates the value types a tunable knob can hold.
type configKind string

const (
	kindInt         configKind = "integer"
	kindBool        configKind = "boolean"
	kindString      configKind = "string"
	kindStringArray configKind = "string[]"
)

// Canonical tunable knob keys. Grouped by the capability they influence.
const (
	// Search defaults (wisdevSearchPapers).
	CfgSearchLimit            = "search.limit"
	CfgSearchQualitySort      = "search.qualitySort"
	CfgSearchMinCitations     = "search.minCitations"
	CfgSearchYearFrom         = "search.yearFrom"
	CfgSearchYearTo           = "search.yearTo"
	CfgSearchExpandQuery      = "search.expandQuery"
	CfgSearchDynamicProviders = "search.dynamicProviders"
	CfgSearchDefaultSources   = "search.defaultSources"
	CfgSearchDefaultDomain    = "search.defaultDomain"

	// Evidence defaults (wisdevEvidenceSearch).
	CfgEvidenceLimit = "evidence.limit"

	// Author defaults (wisdevAuthorSearch).
	CfgAuthorLimit = "author.limit"

	// Manuscript defaults (wisdevGenerateManuscript).
	CfgManuscriptMaxPapers    = "manuscript.maxPapers"
	CfgManuscriptTargetWords  = "manuscript.targetWords"
	CfgManuscriptMinCitations = "manuscript.minCitations"
	CfgManuscriptReviewRounds = "manuscript.reviewRounds"
	CfgManuscriptGenre        = "manuscript.genre"
	CfgManuscriptSectionFlow  = "manuscript.sectionFlow"
	CfgManuscriptFormat       = "manuscript.format"
	CfgManuscriptIntent       = "manuscript.intent"
	CfgManuscriptCitationStyle = "manuscript.citationStyle"

	// Server-level knobs.
	CfgServerTimeoutSeconds = "server.timeoutSeconds"
)

// configKnob declares one tunable parameter: its type, validation envelope,
// default, and a description an external LLM can read to know what it tunes.
type configKnob struct {
	Key         string     `json:"key"`
	Kind        configKind `json:"type"`
	Description string     `json:"description"`
	Group       string     `json:"group"`
	Min         *int       `json:"min,omitempty"`
	Max         *int       `json:"max,omitempty"`
	Enum        []string   `json:"enum,omitempty"`
	Default     any        `json:"default"`
}

func intPtr(v int) *int { return &v }

// knobRegistry is the single source of truth for every tunable MCP knob.
// Order is stable so wisdevGetConfig output is deterministic.
func knobRegistry() []configKnob {
	return []configKnob{
		{Key: CfgSearchLimit, Kind: kindInt, Group: "search", Description: "Default max results for wisdevSearchPapers when 'limit' is omitted.", Min: intPtr(1), Max: intPtr(50), Default: 10},
		{Key: CfgSearchQualitySort, Kind: kindBool, Group: "search", Description: "Default citation-weighted quality sort for searches.", Default: true},
		{Key: CfgSearchMinCitations, Kind: kindInt, Group: "search", Description: "Default minimum citation count filter (0 = no minimum).", Min: intPtr(0), Default: 0},
		{Key: CfgSearchYearFrom, Kind: kindInt, Group: "search", Description: "Default inclusive start-year filter (0 = no floor).", Min: intPtr(0), Default: 0},
		{Key: CfgSearchYearTo, Kind: kindInt, Group: "search", Description: "Default inclusive end-year filter (0 = no ceiling).", Min: intPtr(0), Default: 0},
		{Key: CfgSearchExpandQuery, Kind: kindBool, Group: "search", Description: "Default query-expansion behavior for searches.", Default: false},
		{Key: CfgSearchDynamicProviders, Kind: kindBool, Group: "search", Description: "Default to LLM-driven dynamic provider selection.", Default: false},
		{Key: CfgSearchDefaultSources, Kind: kindStringArray, Group: "search", Description: "Default provider hints when 'sources' is omitted (e.g. openalex, arxiv, pubmed).", Default: []string{}},
		{Key: CfgSearchDefaultDomain, Kind: kindString, Group: "search", Description: "Default research-domain hint for provider routing.", Default: ""},

		{Key: CfgEvidenceLimit, Kind: kindInt, Group: "evidence", Description: "Default max evidence snippets for wisdevEvidenceSearch.", Min: intPtr(1), Max: intPtr(20), Default: 5},

		{Key: CfgAuthorLimit, Kind: kindInt, Group: "author", Description: "Default max results for wisdevAuthorSearch.", Min: intPtr(1), Max: intPtr(50), Default: 20},

		{Key: CfgManuscriptMaxPapers, Kind: kindInt, Group: "manuscript", Description: "Default papers grounding a generated manuscript.", Min: intPtr(1), Max: intPtr(80), Default: 30},
		{Key: CfgManuscriptTargetWords, Kind: kindInt, Group: "manuscript", Description: "Default target word count (0 = model chooses).", Min: intPtr(0), Max: intPtr(20000), Default: 0},
		{Key: CfgManuscriptMinCitations, Kind: kindInt, Group: "manuscript", Description: "Default minimum distinct sources a manuscript must cite when a call passes none (pass an explicit 0 for no minimum).", Min: intPtr(0), Max: intPtr(200), Default: 10},
		{Key: CfgManuscriptReviewRounds, Kind: kindInt, Group: "manuscript", Description: "Default max generate→review→revise rounds (0 = pipeline default of 2).", Min: intPtr(0), Max: intPtr(5), Default: 0},
		{Key: CfgManuscriptGenre, Kind: kindString, Group: "manuscript", Description: "Default manuscript genre, e.g. 'narrative literature review' or 'research paper'.", Default: ""},
		{Key: CfgManuscriptSectionFlow, Kind: kindStringArray, Group: "manuscript", Description: "Default ordered section flow, e.g. [\"abstract\",\"introduction\",\"methods\",\"results\",\"discussion\",\"conclusion\"].", Default: []string{}},
		{Key: CfgManuscriptFormat, Kind: kindString, Group: "manuscript", Description: "Default manuscript output format.", Enum: []string{"markdown", "json", "latex", "html"}, Default: "markdown"},
		{Key: CfgManuscriptIntent, Kind: kindString, Group: "manuscript", Description: "Default document intent: report (quick synthesis), litreview (thematic review), or fullpaper (grounded manuscript pipeline).", Enum: []string{"fullpaper", "report", "litreview"}, Default: "fullpaper"},
		{Key: CfgManuscriptCitationStyle, Kind: kindString, Group: "manuscript", Description: "Default bibliography citation style.", Enum: []string{"apa", "mla", "chicago", "vancouver", "ieee", "harvard", "nature"}, Default: "apa"},

		{Key: CfgServerTimeoutSeconds, Kind: kindInt, Group: "server", Description: "Per-request MCP handler timeout in seconds.", Min: intPtr(5), Max: intPtr(300), Default: 30},
	}
}

// knobByKey indexes the registry for validation lookups.
func knobByKey() map[string]configKnob {
	knobs := knobRegistry()
	out := make(map[string]configKnob, len(knobs))
	for _, k := range knobs {
		out[k.Key] = k
	}
	return out
}

// RuntimeConfig is the mutable, concurrency-safe tuning state for an MCPServer.
// It stores one value per declared knob; unknown keys are rejected on Tune.
type RuntimeConfig struct {
	mu     sync.RWMutex
	values map[string]any
}

// NewRuntimeConfig returns a RuntimeConfig populated with every knob's default.
func NewRuntimeConfig() *RuntimeConfig {
	c := &RuntimeConfig{values: make(map[string]any)}
	c.resetLocked(nil)
	return c
}

// resetLocked sets the given keys (or all keys when keys is empty) to defaults.
// Caller need not hold the lock; this method manages it.
func (c *RuntimeConfig) resetLocked(keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[strings.TrimSpace(k)] = struct{}{}
	}
	for _, knob := range knobRegistry() {
		if len(keys) > 0 {
			if _, ok := want[knob.Key]; !ok {
				continue
			}
		}
		c.values[knob.Key] = cloneConfigValue(knob.Default)
	}
}

// Reset restores defaults for the given keys, or all knobs when keys is empty.
// It returns an error naming any unknown keys (no partial reset is applied for
// those, but valid keys are still reset).
func (c *RuntimeConfig) Reset(keys ...string) error {
	if len(keys) == 0 {
		c.resetLocked(nil)
		return nil
	}
	index := knobByKey()
	var unknown []string
	valid := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if _, ok := index[k]; ok {
			valid = append(valid, k)
		} else if k != "" {
			unknown = append(unknown, k)
		}
	}
	if len(valid) > 0 {
		c.resetLocked(valid)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown config key(s): %s", strings.Join(unknown, ", "))
	}
	return nil
}

// Snapshot returns a copy of all current knob values.
func (c *RuntimeConfig) Snapshot() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]any, len(c.values))
	for k, v := range c.values {
		out[k] = cloneConfigValue(v)
	}
	return out
}

// Tune validates and applies a batch of updates. It returns the values that
// were actually changed (key → new value). A non-nil error describes every
// rejected update; rejected updates are not applied, but valid ones in the same
// batch still are (best-effort partial application keeps tuning forgiving).
func (c *RuntimeConfig) Tune(updates map[string]any) (map[string]any, error) {
	index := knobByKey()
	applied := make(map[string]any)
	var problems []string

	// Coerce + validate everything first so we can report all issues together.
	staged := make(map[string]any, len(updates))
	for rawKey, rawVal := range updates {
		key := strings.TrimSpace(rawKey)
		knob, ok := index[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("%q: unknown config key", rawKey))
			continue
		}
		val, err := coerceAndValidate(knob, rawVal)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%q: %v", key, err))
			continue
		}
		staged[key] = val
	}

	if len(staged) > 0 {
		c.mu.Lock()
		for key, val := range staged {
			c.values[key] = val
			applied[key] = cloneConfigValue(val)
		}
		c.mu.Unlock()
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return applied, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return applied, nil
}

// ───────────────── typed accessors used by the action tools ─────────────────

func (c *RuntimeConfig) Int(key string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch v := c.values[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func (c *RuntimeConfig) Bool(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, _ := c.values[key].(bool)
	return v
}

func (c *RuntimeConfig) String(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, _ := c.values[key].(string)
	return v
}

func (c *RuntimeConfig) Strings(key string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch v := c.values[key].(type) {
	case []string:
		return append([]string(nil), v...)
	default:
		return nil
	}
}

// ───────────────── coercion + validation ─────────────────

// coerceAndValidate converts a raw (often JSON-decoded) value into the knob's
// canonical Go type and enforces its bounds/enum.
func coerceAndValidate(knob configKnob, raw any) (any, error) {
	switch knob.Kind {
	case kindInt:
		n, err := configToInt(raw)
		if err != nil {
			return nil, err
		}
		if knob.Min != nil && n < *knob.Min {
			return nil, fmt.Errorf("must be >= %d (got %d)", *knob.Min, n)
		}
		if knob.Max != nil && n > *knob.Max {
			return nil, fmt.Errorf("must be <= %d (got %d)", *knob.Max, n)
		}
		return n, nil
	case kindBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("must be a boolean")
		}
		return b, nil
	case kindString:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		s = strings.TrimSpace(s)
		if len(knob.Enum) > 0 && s != "" {
			matched := false
			for _, e := range knob.Enum {
				if strings.EqualFold(s, e) {
					s = e
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("must be one of [%s]", strings.Join(knob.Enum, ", "))
			}
		}
		return s, nil
	case kindStringArray:
		return configToStringSlice(raw)
	default:
		return nil, fmt.Errorf("unsupported knob kind %q", knob.Kind)
	}
}

func configToInt(raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("must be a whole number (got %v)", v)
		}
		return int(v), nil
	case float32:
		return int(v), nil
	default:
		return 0, fmt.Errorf("must be an integer")
	}
}

func configToStringSlice(raw any) ([]string, error) {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("must be an array of strings")
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an array of strings")
	}
}

// cloneConfigValue defensively copies slice values so snapshots and stored
// state never alias.
func cloneConfigValue(v any) any {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...)
	default:
		return v
	}
}
