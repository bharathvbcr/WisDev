package wisdev

import (
	"strings"
	"testing"
)

func TestIsUncitedProtocolSentence(t *testing.T) {
	// Uncited protocol claims (any subject phrasing) must flag — the docQ regressions.
	flag := []string{
		"While the analysis utilizes a PRISMA-based search methodology to ensure a systematic evaluation, findings are limited.",
		"This synthesis aggregates 30 peer-reviewed studies identified through a systematic search of PubMed, Embase, and IEEE Xplore.",
		"The literature was screened 4,349 records before final inclusion.",
		"Inclusion criteria required empirical validation across the corpus.",
	}
	for _, s := range flag {
		if !isUncitedProtocolSentence(s) {
			t.Errorf("expected uncited protocol sentence to flag: %q", s)
		}
	}
	// Attributed-to-a-source mentions (citation / et al. / by Author) must NOT flag.
	spare := []string{
		"The systematic review by Smith et al. applied PRISMA 2020 to 30 studies.",
		"One scoping review searched PubMed and Embase for relevant trials [4].",
		"A PRISMA-guided survey identified 89 papers [11].",
		"RAG retrieves documents from a vector database to ground its outputs.",
		"This review synthesizes findings on clinical decision support [2].",
	}
	for _, s := range spare {
		if isUncitedProtocolSentence(s) {
			t.Errorf("attributed/topical sentence must NOT flag: %q", s)
		}
	}
	// The robust content-level check catches the new-subject phrasings the subject/verb
	// patterns miss.
	if !contentClaimsOwnMethodology("The analysis utilizes a PRISMA-based search methodology across databases.") {
		t.Errorf("content check should catch an uncited PRISMA claim regardless of subject")
	}
}

func TestVoiceAndProtocolGapsFromDocS(t *testing.T) {
	// First-person review-process verbs must flag as primary-research voice.
	for _, s := range []string{
		"We identified relevant studies across the corpus.",
		"We examined the reported outcomes of each model.",
		"We screened candidate papers for empirical validation.",
	} {
		if !containsPrimaryResearchVoice(s) {
			t.Errorf("expected first-person voice to flag: %q", s)
		}
	}
	// "systematic literature review" / "SLR" are protocol terms; uncited -> flag.
	for _, s := range []string{
		"This work is a systematic literature review covering 30 studies.",
		"The SLR covering 30 studies identified key themes.",
	} {
		if !isUncitedProtocolSentence(s) {
			t.Errorf("expected uncited SLR/systematic-literature-review to flag: %q", s)
		}
	}
	// Attributed SLR is spared.
	if isUncitedProtocolSentence("A systematic literature review of 30 studies by Lee et al. categorized RAG approaches [5].") {
		t.Errorf("attributed SLR must not flag")
	}
	// 'review' as a normal verb must NOT trigger primary-research voice when not first-person.
	if containsPrimaryResearchVoice("Prior work reviews retrieval-augmented architectures for clinical use [2].") {
		t.Errorf("third-person 'reviews' wrongly flagged")
	}
}

func TestStripSelfMethodologySentences(t *testing.T) {
	content := "Retrieval-augmented generation grounds outputs in retrieved evidence [1]. This review followed PRISMA guidelines to identify, select, and evaluate studies. Clinician trust depends on transparent, citation-backed responses [3]."
	out := stripSelfMethodologySentences(content)
	if claimsOwnSystematicMethodology(out) {
		t.Fatalf("self-methodology sentence not removed: %q", out)
	}
	if !strings.Contains(out, "grounds outputs in retrieved evidence [1]") || !strings.Contains(out, "Clinician trust") {
		t.Fatalf("legitimate sentences were dropped: %q", out)
	}
	// Clean prose is unchanged.
	clean := "RAG grounds outputs in retrieved evidence [1]. Clinician trust depends on transparency [3]."
	if got := stripSelfMethodologySentences(clean); got != clean {
		t.Fatalf("clean prose altered: %q", got)
	}
	// Never empties a section even if every sentence matched.
	allBad := "This review employed a PRISMA-compliant search strategy across five databases."
	if got := stripSelfMethodologySentences(allBad); got != allBad {
		t.Fatalf("should not empty a section: %q", got)
	}
}

func TestClaimsOwnSystematicMethodology(t *testing.T) {
	positives := []string{
		"This review employs a PRISMA-compliant search strategy to identify and synthesize literature on RAG.",
		"Utilizing a PRISMA-compliant search strategy to evaluate the field, several themes emerge.",
		"The present study conducted a systematic database search across five sources.",
		"Our review applied strict inclusion criteria to screen candidate studies.",
		"This analysis follows PRISMA 2020 guidelines for study selection.",
		"The search strategy focused on peer-reviewed databases published through 2024.",
		"Search strategies utilized peer-reviewed literature across multiple databases.",
		// Forms observed surviving earlier passes (docM).
		"This narrative review was conducted to synthesize the current evidence regarding RAG.",
		"This narrative review was conducted by querying PubMed and Scopus for recent literature.",
		"This review synthesizes 30 peer-reviewed studies identified through a systematic search of databases.",
		"The selection criteria focused on peer-reviewed literature with empirical validation.",
		"Inclusion criteria mandated the presence of empirical performance data.",
	}
	for _, s := range positives {
		if !claimsOwnSystematicMethodology(s) {
			t.Errorf("expected self-attributed methodology to be flagged: %q", s)
		}
	}
	// Methods-subsection headings must be detected by the heading stripper.
	headings := []string{
		"### Search Strategy and Selection Criteria",
		"**Search Strategy and Selection Criteria**",
		"## Methodology",
	}
	for _, h := range headings {
		if !methodsSubsectionHeadingPattern.MatchString(h) {
			t.Errorf("expected methods heading to match: %q", h)
		}
	}
	// A prose line merely starting with one of these words (no heading marker) must NOT match.
	if methodsSubsectionHeadingPattern.MatchString("Methods for grounding LLMs include retrieval augmentation.") {
		t.Errorf("plain prose line wrongly matched as a methods heading")
	}
	// Negatives — a protocol correctly ATTRIBUTED to a cited source must NOT flag, nor
	// must ordinary narrative-review prose.
	negatives := []string{
		"The systematic review by Smith et al. [3] applied PRISMA 2020 to 30 studies.",
		"Prior work [2] conducted a database search across five databases.",
		"This review synthesizes findings on retrieval-augmented generation in clinical decision support.",
		"One study reported that a PRISMA-guided search identified 89 papers [11].",
		"Several authors propose retrieval-augmented architectures for grounding [4].",
		"A RAG system's search strategy retrieves documents from a vector database at query time.",
		"The search strategy of modern retrieval systems queries dense embeddings for relevant passages.",
		// Legitimate narrative-review self-reference (synthesize/examine/discuss) must NOT flag.
		"This review synthesizes findings and examines the clinical implications of grounded generation.",
		"This narrative review discusses the limitations of zero-shot prompting in clinical settings.",
		// Topical 'criteria' / 'selection' about the systems under review, not the review's own.
		"Model selection criteria for deployment prioritize latency and on-premises privacy.",
	}
	for _, s := range negatives {
		if claimsOwnSystematicMethodology(s) {
			t.Errorf("attributed/ordinary prose must NOT be flagged: %q", s)
		}
	}
}

func TestOurMethodologicalDesignVoice(t *testing.T) {
	for _, s := range []string{
		"Our methodological design prioritized empirical validation.",
		"Our protocol required two independent reviewers.",
		"Our evaluation framework assessed factuality and safety.",
	} {
		if !containsPrimaryResearchVoice(s) {
			t.Errorf("expected first-person 'our ...' to flag: %q", s)
		}
	}
	// Third-person 'the methodological design of X' must NOT flag.
	if containsPrimaryResearchVoice("The methodological design of the Almanac framework emphasized expert review [2].") {
		t.Errorf("third-person methodological design wrongly flagged")
	}
}
