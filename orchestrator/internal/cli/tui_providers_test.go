package cli

import "testing"

func TestMaybeApplyAutoProviderPresetMedicine(t *testing.T) {
	state := &tuiState{
		detectedDomain: "medicine",
		providers: []tuiProvider{
			{code: "pubmed", enabled: true},
			{code: "arxiv", enabled: true},
			{code: "ieee", enabled: true},
			{code: "dblp", enabled: true},
			{code: "repec", enabled: true},
			{code: "ssrn", enabled: true},
			{code: "nasa_ads", enabled: true},
			{code: "philpapers", enabled: true},
			{code: "papers_with_code", enabled: true},
		},
	}
	state.maybeApplyAutoProviderPreset()
	if !state.biomedicalPresetActive() {
		t.Fatal("expected medicine auto-preset to keep only biomedical providers")
	}
}

func TestMaybeApplyAutoProviderPresetCS(t *testing.T) {
	state := &tuiState{
		detectedDomain: "computer science",
		providers: []tuiProvider{
			{code: "dblp", enabled: true},
			{code: "arxiv", enabled: true},
			{code: "pubmed", enabled: true},
			{code: "ieee", enabled: true},
			{code: "repec", enabled: true},
			{code: "ssrn", enabled: true},
			{code: "nasa_ads", enabled: true},
			{code: "philpapers", enabled: true},
			{code: "papers_with_code", enabled: true},
		},
	}
	state.maybeApplyAutoProviderPreset()
	if !state.csPresetActive() {
		t.Fatal("expected cs auto-preset to keep only cs providers")
	}
}

func TestToggleCSProviderPreset(t *testing.T) {
	state := &tuiState{
		providers: []tuiProvider{
			{code: "dblp", enabled: false},
			{code: "pubmed", enabled: true},
			{code: "arxiv", enabled: true},
		},
	}
	state.toggleCSProviderPreset()
	if !state.providers[0].enabled || state.providers[1].enabled || !state.providers[2].enabled {
		t.Fatalf("expected cs-only preset, got %#v", state.providers)
	}
}

func TestToggleBiomedicalProviderPreset(t *testing.T) {
	state := &tuiState{
		providers: []tuiProvider{
			{code: "pubmed", enabled: false},
			{code: "arxiv", enabled: true},
			{code: "openalex", enabled: true},
		},
	}
	state.toggleBiomedicalProviderPreset()
	if !state.providers[0].enabled || state.providers[1].enabled {
		t.Fatalf("expected biomedical-only preset, got %#v", state.providers)
	}
	state.toggleBiomedicalProviderPreset()
	if !state.providers[0].enabled || !state.providers[1].enabled || !state.providers[2].enabled {
		t.Fatalf("expected all providers after second toggle, got %#v", state.providers)
	}
}

func TestTogglePhysicsProviderPreset(t *testing.T) {
	state := &tuiState{
		providers: []tuiProvider{
			{code: "arxiv", enabled: false},
			{code: "pubmed", enabled: true},
			{code: "openalex", enabled: true},
		},
	}
	state.togglePhysicsProviderPreset()
	if !state.providers[0].enabled || state.providers[1].enabled {
		t.Fatalf("expected physics-only preset, got %#v", state.providers)
	}
	state.togglePhysicsProviderPreset()
	if !state.providers[0].enabled || !state.providers[1].enabled || !state.providers[2].enabled {
		t.Fatalf("expected all providers after second toggle, got %#v", state.providers)
	}
}

func TestToggleGeneralProviderPreset(t *testing.T) {
	state := &tuiState{
		providers: []tuiProvider{
			{code: "openalex", enabled: false},
			{code: "pubmed", enabled: true},
			{code: "crossref", enabled: true},
		},
	}
	state.toggleGeneralProviderPreset()
	if !state.providers[0].enabled || state.providers[1].enabled || !state.providers[2].enabled {
		t.Fatalf("expected general-only preset, got %#v", state.providers)
	}
}

func TestTogglePreprintProviderPreset(t *testing.T) {
	state := &tuiState{
		providers: []tuiProvider{
			{code: "biorxiv", enabled: false},
			{code: "pubmed", enabled: true},
			{code: "arxiv", enabled: true},
		},
	}
	state.togglePreprintProviderPreset()
	if !state.providers[0].enabled || state.providers[1].enabled || !state.providers[2].enabled {
		t.Fatalf("expected preprint-only preset, got %#v", state.providers)
	}
}

