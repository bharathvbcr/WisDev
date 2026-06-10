package cli

import (
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

var biomedicalProviderCodes = map[string]struct{}{
	"pubmed":           {},
	"europe_pmc":       {},
	"semantic_scholar": {},
	"openalex":         {},
	"biorxiv":          {},
	"medrxiv":          {},
	"clinical_trials":  {},
	"crossref":         {},
	"doaj":             {},
}

var physicsProviderCodes = map[string]struct{}{
	"arxiv":            {},
	"semantic_scholar": {},
	"openalex":         {},
	"crossref":         {},
	"doaj":             {},
}

var generalProviderCodes = map[string]struct{}{
	"semantic_scholar": {},
	"openalex":         {},
	"crossref":         {},
	"doaj":             {},
}

var preprintProviderCodes = map[string]struct{}{
	"arxiv":   {},
	"biorxiv": {},
	"medrxiv": {},
}

var csProviderCodes = map[string]struct{}{
	"dblp":             {},
	"arxiv":            {},
	"semantic_scholar": {},
	"papers_with_code": {},
	"openalex":         {},
	"ieee":             {},
	"crossref":         {},
	"core":             {},
}

func (s *tuiState) presetActive(codes map[string]struct{}) bool {
	if s == nil {
		return false
	}
	enabled := 0
	presetEnabled := 0
	for _, p := range s.providers {
		if !p.enabled {
			continue
		}
		enabled++
		if _, ok := codes[p.code]; ok {
			presetEnabled++
		}
	}
	return enabled > 0 && enabled == presetEnabled
}

func (s *tuiState) togglePreset(codes map[string]struct{}) {
	if s == nil {
		return
	}
	active := s.presetActive(codes)
	for idx := range s.providers {
		_, isMatch := codes[s.providers[idx].code]
		switch {
		case s.offlineMode:
			s.providers[idx].enabled = false
		case active:
			s.providers[idx].enabled = true
		default:
			s.providers[idx].enabled = isMatch
		}
	}
}

func (s *tuiState) biomedicalPresetActive() bool {
	return s.presetActive(biomedicalProviderCodes)
}

func (s *tuiState) toggleBiomedicalProviderPreset() {
	s.togglePreset(biomedicalProviderCodes)
}

func (s *tuiState) physicsPresetActive() bool {
	return s.presetActive(physicsProviderCodes)
}

func (s *tuiState) togglePhysicsProviderPreset() {
	s.togglePreset(physicsProviderCodes)
}

func (s *tuiState) generalPresetActive() bool {
	return s.presetActive(generalProviderCodes)
}

func (s *tuiState) toggleGeneralProviderPreset() {
	s.togglePreset(generalProviderCodes)
}

func (s *tuiState) preprintPresetActive() bool {
	return s.presetActive(preprintProviderCodes)
}

func (s *tuiState) togglePreprintProviderPreset() {
	s.togglePreset(preprintProviderCodes)
}

func (s *tuiState) csPresetActive() bool {
	return s.presetActive(csProviderCodes)
}

func (s *tuiState) toggleCSProviderPreset() {
	s.togglePreset(csProviderCodes)
}

func providerPresetCodes(preset string) (map[string]struct{}, bool) {
	switch preset {
	case "biomedical":
		return biomedicalProviderCodes, true
	case "cs":
		return csProviderCodes, true
	case "physics":
		return physicsProviderCodes, true
	case "general":
		return generalProviderCodes, true
	case "preprints":
		return preprintProviderCodes, true
	default:
		return nil, false
	}
}

func (s *tuiState) maybeApplyAutoProviderPreset() {
	if s == nil || s.offlineMode || s.autoDomainPresetApplied {
		return
	}
	preset, ok := search.ProviderPresetForDomain(s.detectedDomain)
	if !ok {
		return
	}
	codes, ok := providerPresetCodes(preset)
	if !ok {
		return
	}
	if s.presetActive(codes) {
		s.autoDomainPresetApplied = true
		return
	}
	if s.enabledProviderCount() <= 8 {
		return
	}
	s.togglePreset(codes)
	s.autoDomainPresetApplied = true
	s.addLog(fmt.Sprintf("%s domain detected — applied %s provider preset.", s.detectedDomain, preset), "I")
}

func explainSynthesisModeHint(mode, llmBackend string) string {
	if strings.TrimSpace(mode) != "heuristic" {
		return ""
	}
	backend := strings.TrimSpace(llmBackend)
	if backend == "" || backend == "sidecar" {
		return "Heuristic synthesis used — no direct LLM provider wired. Set WISDEV_LLM_PROVIDER=ollama|cloud|hybrid, run Ollama, configure Vertex, or start the orchestrator sidecar."
	}
	return "Heuristic synthesis used — LLM backend " + backend + " was unavailable or rate-limited for this run. Check logs for synthesize_with_evidence degradation."
}

func explainStopReasonForState(reason string, exhaustive bool, iterations, requested int) string {
	if exhaustive && requested > 0 && iterations < requested {
		return fmt.Sprintf("Exhaustive run stopped at %d/%d iterations — likely pending-query exhaustion; raise max iterations or widen providers.", iterations, requested)
	}
	return explainStopReason(reason)
}

func explainStopReason(reason string) string {
	switch reason {
	case "coverage_open", "claim_coverage_open":
		return "Coverage is still open; enable Exhaustive mode or raise max iterations for deeper retrieval."
	case "belief convergence", "converged":
		return "The belief loop judged evidence sufficient for the current budget."
	case "search budget exhausted before beliefs formed", "search budget exhausted with no active beliefs":
		return "Search budget ran out before hypotheses stabilized; raise max iterations."
	default:
		return ""
	}
}
