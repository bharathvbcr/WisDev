package search

func BuildRegistry(requestedProviders ...string) *ProviderRegistry {
	allow := map[string]bool{}
	filtered := len(requestedProviders) > 0
	for _, name := range requestedProviders {
		allow[name] = true
	}

	reg := NewProviderRegistry()
	addProvider := func(name string, provider SearchProvider) {
		if filtered {
			if !allow[name] {
				return
			}
		}
		reg.Register(provider)
	}

	addProvider("semantic_scholar", NewSemanticScholarProvider())
	addProvider("openalex", NewOpenAlexProvider())
	addProvider("pubmed", NewPubMedProvider())
	addProvider("core", NewCOREProvider())
	addProvider("arxiv", NewArXivProvider())
	addProvider("biorxiv", NewBioRxivProvider())
	addProvider("medrxiv", NewMedRxivProvider())
	addProvider("europe_pmc", NewEuropePMCProvider())
	addProvider("clinical_trials", NewClinicalTrialsProvider())
	addProvider("crossref", NewCrossrefProvider())
	addProvider("dblp", NewDBLPProvider())
	addProvider("papers_with_code", NewPapersWithCodeProvider())
	addProvider("google_scholar", NewGoogleScholarProvider())
	addProvider("doaj", NewDOAJProvider())
	addProvider("ssrn", NewSSRNProvider())
	addProvider("ieee", NewIEEEProvider())
	addProvider("nasa_ads", NewNASAADSProvider())
	addProvider("repec", NewRePECProvider())
	addProvider("philpapers", NewPhilPapersProvider())

	ApplyDomainRoutes(reg)
	reg.SetDB(nil)
	return reg
}
