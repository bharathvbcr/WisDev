package cli

// docgen.go implements `wisdev docgen` (alias: docugen): a one-shot
// "search + DocuGen" command. It runs the local research loop to gather grounded
// papers, feeds them through the manuscript pipeline (the same engine the
// /full-paper HTTP route drives), and renders a grounded manuscript draft as
// Markdown (or raw JSON). Section enrichment uses the Python sidecar when one is
// reachable and falls back to grounded scaffolds when it is not, so the command
// also works fully offline.
//
// NOTE: "paper" is deliberately NOT an alias — a bare question starting with the
// word "paper" ("paper on transformers") must stay a search (see docgen_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/citations"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/docgen"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/telemetry"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

type docGenOptions struct {
	query            string
	intent           string // "report" | "litreview" | "fullpaper"
	citationStyle    string // "apa" | "mla" | ... (see internal/citations)
	format           string // "markdown" | "latex" | "html" | "docx" | "json"
	jsonOut          bool
	quiet            bool
	verbose          bool
	showStages       bool
	offline          bool
	providers        []string
	domain           string
	pythonURL        string
	outputPath       string
	timeout          time.Duration
	maxIterations    int
	maxSearchTerms   int
	hitsPerSearch    int
	maxUniquePapers  int
	disableEnhance   bool
	logFile          string
	corpusDump       string
	corpusFile       string
	targetWords      int      // --words: manuscript-wide target word count (0 = model default)
	minCitations     int      // --min-citations: minimum distinct sources to cite (0 = none)
	sectionFlow      []string // --flow: ordered section plan (empty = default)
	reviewRounds     int      // --review-rounds: agentic review→revise loop budget (0 = default)
	genre            string   // --genre: manuscript genre (empty = narrative literature review)
	allReferences    bool     // --all-references: list every retrieved source, not only cited ones
	autoMinCitations bool     // true when minCitations came from the smart default, not a flag
}

// manuscriptControls holds the granular DocGen knobs shared by the `docgen` and
// `yolo --docgen` paths so both apply them to the pipeline identically.
type manuscriptControls struct {
	targetWords  int
	minCitations int
	sectionFlow  []string
	reviewRounds int
	genre        string
}

// applyManuscriptControls sets the optional granular knobs on a pipeline. Zero/empty
// values are left at the pipeline default.
func applyManuscriptControls(p *internalwisdev.ManuscriptPipeline, c manuscriptControls) {
	if c.targetWords > 0 {
		p.TargetWords = c.targetWords
	}
	if c.minCitations > 0 {
		p.MinCitations = c.minCitations
	}
	if len(c.sectionFlow) > 0 {
		p.SectionFlow = c.sectionFlow
	}
	if c.reviewRounds > 0 {
		p.ReviewRounds = c.reviewRounds
	}
	if g := strings.TrimSpace(c.genre); g != "" {
		p.Genre = g
	}
}

func runDocGen(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("docgen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "emit the raw manuscript pipeline result as JSON")
	quiet := fs.Bool("quiet", false, "print only the manuscript markdown")
	verbose := fs.Bool("verbose", false, "show search queries and pipeline stages on stderr")
	fs.BoolVar(jsonOut, "j", false, "emit the raw manuscript pipeline result as JSON")
	fs.BoolVar(quiet, "q", false, "print only the manuscript markdown")
	fs.BoolVar(verbose, "v", false, "show search queries and pipeline stages on stderr")
	output := fs.String("output", "", "write the manuscript to this file instead of stdout")
	fs.StringVar(output, "o", "", "write the manuscript to this file instead of stdout")
	format := fs.String("format", "", "output format: markdown|latex|html|docx|json (default: inferred from -o extension, else markdown)")
	fs.StringVar(format, "f", "", "output format: markdown|latex|html|docx|json")
	intent := fs.String("intent", "", "document type: report|litreview|fullpaper (default: fullpaper)")
	citationStyle := fs.String("citation-style", "", "bibliography citation style: apa|mla|chicago|vancouver|ieee|harvard|nature (default: apa)")
	offline := fs.Bool("offline", false, "disable network search providers (grounded scaffold only)")
	stages := fs.Bool("stages", false, "stream search-loop stage events to stderr")
	providers := fs.String("provider", "", "comma-separated built-in provider names for retrieval")
	domain := fs.String("domain", "", "research domain hint (e.g. medicine, cs)")
	pythonURL := fs.String("python-url", "", "Python sidecar base URL for section enrichment (default: resolved)")
	timeout := fs.Duration("timeout", 10*time.Minute, "overall timeout")
	maxIterations := fs.Int("max-iterations", defaultLocalMaxIterations(3), "maximum search-loop iterations")
	maxSearchTerms := fs.Int("max-search-terms", 6, "maximum search terms")
	hitsPerSearch := fs.Int("hits-per-search", 5, "results requested per provider query")
	maxUniquePapers := fs.Int("max-unique-papers", 30, "cap on unique papers fed into the manuscript")
	noEnhance := fs.Bool("no-enhance", false, "disable AI query grammar/typo/acronym preparation")
	logFile := fs.String("log-file", "", "write run logs to this file instead of stderr")
	corpusDump := fs.String("corpus-dump", "", "after retrieval, write the retrieved papers as JSON to this file (for reproducible re-runs)")
	corpusFile := fs.String("corpus-file", "", "load papers from this JSON file (written by -corpus-dump) instead of live retrieval — gives a fixed corpus so prompt/pipeline changes can be A/B compared without retrieval variance")
	words := fs.Int("words", 0, "target total word count for the manuscript (split across sections; 0 = model default)")
	minCitations := fs.Int("min-citations", 0, "minimum number of distinct sources the manuscript should cite (0 = no minimum)")
	flow := fs.String("flow", "", "comma-separated section flow, e.g. introduction,methods,results,discussion (empty = default plan)")
	reviewRounds := fs.Int("review-rounds", 0, "max rounds of the agentic generate→review→revise loop (0 = default 2, max 5)")
	genre := fs.String("genre", "", "manuscript genre, e.g. \"research paper\" (default: narrative literature review)")
	allReferences := fs.Bool("all-references", false, "list every retrieved source in the bibliography, not just in-text-cited ones (default on for --corpus-file)")

	query, err := parseInterspersedDocGenArgs(fs, args)
	if err != nil {
		return err
	}
	if query == "" {
		return errors.New("missing research question: usage: wisdev docgen [flags] \"your topic\"")
	}

	resolvedFormat, err := resolveDocGenFormat(*format, *output, *jsonOut)
	if err != nil {
		return err
	}

	// Validate intent + citation style up front so a typo fails fast before the
	// (expensive) research loop runs. The canonical parsers live in the docgen
	// and citations packages so every surface accepts the same vocabulary.
	parsedIntent, err := docgen.ParseIntent(*intent)
	if err != nil {
		return err
	}
	parsedStyle, err := citations.ParseStyle(*citationStyle)
	if err != nil {
		return err
	}

	// Smart default: a manuscript with no citation floor reads thin. When the
	// caller passes no --min-citations (and is not replaying a fixed corpus),
	// apply a sane baseline; the explicit flag always wins.
	autoMinCitations := false
	if *minCitations == 0 && strings.TrimSpace(*corpusFile) == "" {
		*minCitations = smartDocGenMinCitations
		autoMinCitations = true
	}

	return runDocGenWithOptions(stdout, stderr, docGenOptions{
		query:            query,
		intent:           string(parsedIntent),
		citationStyle:    string(parsedStyle),
		format:           resolvedFormat,
		jsonOut:          *jsonOut,
		quiet:            *quiet,
		verbose:          *verbose,
		showStages:       *stages || *verbose,
		offline:          *offline,
		providers:        splitCSV(*providers),
		domain:           *domain,
		pythonURL:        strings.TrimSpace(*pythonURL),
		outputPath:       strings.TrimSpace(*output),
		timeout:          *timeout,
		maxIterations:    *maxIterations,
		maxSearchTerms:   *maxSearchTerms,
		hitsPerSearch:    *hitsPerSearch,
		maxUniquePapers:  *maxUniquePapers,
		disableEnhance:   *noEnhance,
		logFile:          strings.TrimSpace(*logFile),
		corpusDump:       strings.TrimSpace(*corpusDump),
		corpusFile:       strings.TrimSpace(*corpusFile),
		targetWords:      *words,
		minCitations:     *minCitations,
		sectionFlow:      splitCSV(*flow),
		reviewRounds:     *reviewRounds,
		genre:            strings.TrimSpace(*genre),
		allReferences:    *allReferences,
		autoMinCitations: autoMinCitations,
	})
}

// parseInterspersedDocGenArgs lets flags appear AFTER the positional query (Go's
// flag package otherwise stops at the first non-flag token and folds the flags
// into the query) by re-parsing after each positional. An explicit `--` ends flag
// parsing: everything after it is literal, so a dash-leading topic works via
// `docgen -- -my weird topic`.
func parseInterspersedDocGenArgs(fs *flag.FlagSet, args []string) (string, error) {
	flagArgs := args
	var literalTail []string
	for i, a := range args {
		if a == "--" {
			flagArgs, literalTail = args[:i], args[i+1:]
			break
		}
	}
	positionals := make([]string, 0, len(args))
	rest := flagArgs
	for {
		if err := fs.Parse(rest); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	positionals = append(positionals, literalTail...)
	return strings.TrimSpace(strings.Join(positionals, " ")), nil
}

func runDocGenWithOptions(stdout, stderr io.Writer, opts docGenOptions) error {
	// Retrieve at least as many papers as the requested citation floor so the
	// manuscript can actually cite that many distinct sources.
	if opts.minCitations > opts.maxUniquePapers {
		opts.maxUniquePapers = opts.minCitations
	}
	// Reserve stdout for the manuscript (markdown/JSON): route the research and
	// pipeline logs to stderr so `wisdev docgen "x" > paper.md` yields a clean
	// document. The repo's default slog handler writes to stdout (see the MCP
	// stdio logging fix), which would otherwise pollute the document stream.
	logLevel := slog.LevelWarn
	if opts.verbose {
		logLevel = slog.LevelInfo
	}
	logSink := stderr
	if logSink == nil {
		logSink = io.Discard
	}
	if opts.logFile != "" {
		f, err := os.OpenFile(opts.logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("failed to open --log-file %s: %w", opts.logFile, err)
		}
		defer f.Close()
		logSink = f
	}
	logHandler := slog.NewJSONHandler(logSink, &slog.HandlerOptions{Level: logLevel})
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(logHandler))
	defer slog.SetDefault(previousLogger)
	// Search providers log via the telemetry global, NOT slog.Default — reroute it
	// too, otherwise provider JSON logs print to stdout and corrupt the manuscript.
	previousTelemetry := telemetry.Logger()
	telemetry.SetLogger(slog.New(logHandler))
	defer telemetry.SetLogger(previousTelemetry)

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !opts.jsonOut && !opts.quiet {
		printSection(stderr, "WisDev DocuGen")
		note(stderr, "  topic: %s", opts.query)
		switch {
		case opts.offline:
			note(stderr, "  retrieval: offline (grounded scaffold)")
		case len(opts.providers) > 0:
			note(stderr, "  providers: %s", strings.Join(opts.providers, ", "))
		default:
			note(stderr, "  providers: all built-in")
		}
		if opts.autoMinCitations {
			note(stderr, "  citations: auto floor of %d distinct sources (override with --min-citations)", opts.minCitations)
		}
		preflightSidecar(stderr, opts)
		fmt.Fprintln(stderr)
	}

	var (
		papers   []search.Paper
		research *agent.YOLOResult
		err      error
	)
	if opts.corpusFile != "" {
		// Replay a fixed corpus: skip live retrieval so prompt/pipeline changes can
		// be measured on an identical paper set.
		papers, err = loadCorpusPapers(opts.corpusFile)
		if err != nil {
			return fmt.Errorf("load corpus file: %w", err)
		}
		// Build references from the loaded corpus papers (which carry clean
		// author/venue/title metadata) instead of the canonical-source resolution,
		// which mangles them in corpus-file mode (author-less, garbage titles).
		if len(papers) > 0 {
			research = &agent.YOLOResult{Papers: agent.PublicPapersFromSearch(papers)}
		}
		note(stderr, "  corpus: replayed %d papers from %s (retrieval skipped)", len(papers), opts.corpusFile)
	} else {
		papers, research, err = docGenRetrievePapers(ctx, stderr, opts)
		if err != nil {
			return fmt.Errorf("evidence retrieval failed: %w", err)
		}
		if opts.corpusDump != "" {
			if derr := dumpCorpusPapers(opts.corpusDump, papers); derr != nil {
				note(stderr, "  corpus dump failed: %v", derr)
			} else {
				note(stderr, "  corpus: dumped %d papers to %s", len(papers), opts.corpusDump)
			}
		}
	}
	if len(papers) == 0 && !opts.jsonOut && !opts.quiet {
		note(stderr, "  no papers retrieved — emitting a grounded scaffold manuscript")
	}

	// Generation now flows through internal/docgen: it dispatches by intent
	// (report / litreview / fullpaper), owns the manuscript pipeline for the
	// full-paper path, and produces the canonical Document envelope that every
	// surface (CLI / TUI / MCP) renders. Offline mode keeps the pipeline off the
	// sidecar so the run does zero network I/O (sections fall back to scaffolds).
	renderFormat, err := docgen.ParseRenderFormat(opts.format)
	if err != nil {
		return err
	}
	style, err := citations.ParseStyle(opts.citationStyle)
	if err != nil {
		return err
	}
	intent, err := docgen.ParseIntent(opts.intent)
	if err != nil {
		return err
	}

	var genResult docgen.GenerateResult
	err = runWithProgressUpdates(stderr, "Extracting evidence", func(update func(string)) error {
		var genErr error
		genResult, genErr = docgen.Generate(ctx, docgen.Options{
			Query:          opts.query,
			Intent:         intent,
			CitationStyle:  style,
			Papers:         papers,
			Research:       research,
			PythonURL:      opts.pythonURL,
			Offline:        opts.offline,
			IncludeUncited: includeUncitedReferences(opts),
			Manuscript: docgen.ManuscriptControls{
				TargetWords:  opts.targetWords,
				MinCitations: opts.minCitations,
				SectionFlow:  opts.sectionFlow,
				ReviewRounds: opts.reviewRounds,
				Genre:        opts.genre,
			},
			LLMClient: resolveResearchLLMClient(),
			OnStage:   docGenProgressNotifier(update),
			JobID:     fmt.Sprintf("docgen_%d", time.Now().UnixMilli()),
		})
		return genErr
	})
	if err != nil {
		return fmt.Errorf("manuscript generation failed: %w", err)
	}
	result := genResult.Pipeline

	var (
		rendered string
		binary   []byte
	)
	switch {
	case renderFormat == docgen.FormatJSON && intent == docgen.IntentFullPaper:
		// Backward compat: full-paper JSON emits the raw manuscript pipeline
		// result (the shape callers scripted against before the docgen port).
		encoded, encErr := json.MarshalIndent(result, "", "  ")
		if encErr != nil {
			return encErr
		}
		rendered = string(encoded)
	case renderFormat == docgen.FormatDOCX:
		data, _, expErr := docgen.ExportBytes(genResult.Document, docgen.RenderOptions{Format: docgen.FormatDOCX, CitationStyle: style})
		if expErr != nil {
			return expErr
		}
		binary = data
	default:
		text, rErr := docgen.Render(genResult.Document, docgen.RenderOptions{Format: renderFormat, CitationStyle: style})
		if rErr != nil {
			return rErr
		}
		rendered = text
	}

	switch {
	case len(binary) > 0:
		outPath := opts.outputPath
		if outPath == "" {
			outPath = docGenDefaultFilename(opts.query, opts.format, time.Now())
		}
		if err := os.WriteFile(outPath, binary, 0o644); err != nil {
			return fmt.Errorf("failed to write manuscript to %s: %w", outPath, err)
		}
		note(stderr, "  manuscript (%s) saved → %s", opts.format, absPathOrSelf(outPath))
	case opts.outputPath != "":
		if err := os.WriteFile(opts.outputPath, []byte(rendered), 0o644); err != nil {
			return fmt.Errorf("failed to write manuscript to %s: %w", opts.outputPath, err)
		}
		note(stderr, "  manuscript (%s) saved → %s", opts.format, absPathOrSelf(opts.outputPath))
	default:
		fmt.Fprintln(stdout, rendered)
	}

	// The pipeline summary only makes sense for the full-paper intent (report and
	// litreview do not run the manuscript pipeline).
	if !opts.quiet && opts.format != "json" && intent == docgen.IntentFullPaper {
		printDocGenSummary(stderr, result)
	}
	return nil
}

// generateManuscriptFromResearch runs the manuscript pipeline on an existing YOLO
// research result and renders the document in the requested format. It reuses the
// same pipeline + renderers as `wisdev docgen`, so a `wisdev yolo --docgen` run
// produces an identical manuscript WITHOUT a second retrieval pass — it grounds on
// the papers the YOLO loop already gathered. Pass pythonURL="" to auto-resolve the
// sidecar; set offline=true to force grounded-scaffold prose with zero network I/O.
func generateManuscriptFromResearch(
	ctx context.Context,
	stderr io.Writer,
	query string,
	research *agent.YOLOResult,
	intent, format, citationStyle, pythonURL string,
	offline bool,
	controls manuscriptControls,
) (string, internalwisdev.ManuscriptPipelineResult, error) {
	papers := docGenPapersFromResult(research)

	parsedIntent, err := docgen.ParseIntent(intent)
	if err != nil {
		return "", internalwisdev.ManuscriptPipelineResult{}, err
	}
	renderFormat, err := docgen.ParseRenderFormat(format)
	if err != nil {
		return "", internalwisdev.ManuscriptPipelineResult{}, err
	}
	style, err := citations.ParseStyle(citationStyle)
	if err != nil {
		return "", internalwisdev.ManuscriptPipelineResult{}, err
	}

	var genResult docgen.GenerateResult
	err = runWithProgressUpdates(stderr, "Extracting evidence", func(update func(string)) error {
		var genErr error
		genResult, genErr = docgen.Generate(ctx, docgen.Options{
			Query:         query,
			Intent:        parsedIntent,
			CitationStyle: style,
			Papers:        papers,
			Research:      research,
			PythonURL:     pythonURL,
			Offline:       offline,
			Manuscript: docgen.ManuscriptControls{
				TargetWords:  controls.targetWords,
				MinCitations: controls.minCitations,
				SectionFlow:  controls.sectionFlow,
				ReviewRounds: controls.reviewRounds,
				Genre:        controls.genre,
			},
			LLMClient: resolveResearchLLMClient(),
			OnStage:   docGenProgressNotifier(update),
			JobID:     fmt.Sprintf("docgen_%d", time.Now().UnixMilli()),
		})
		return genErr
	})
	if err != nil {
		return "", genResult.Pipeline, err
	}
	result := genResult.Pipeline

	// Full-paper JSON keeps emitting the raw pipeline result for backward compat.
	if renderFormat == docgen.FormatJSON && parsedIntent == docgen.IntentFullPaper {
		encoded, encErr := json.MarshalIndent(result, "", "  ")
		if encErr != nil {
			return "", result, encErr
		}
		return string(encoded), result, nil
	}
	// DOCX is binary; Go strings are byte-preserving so os.WriteFile([]byte(s))
	// round-trips the bytes exactly (docx always writes to a file, never stdout).
	if renderFormat == docgen.FormatDOCX {
		data, _, expErr := docgen.ExportBytes(genResult.Document, docgen.RenderOptions{Format: docgen.FormatDOCX, CitationStyle: style})
		if expErr != nil {
			return "", result, expErr
		}
		return string(data), result, nil
	}
	rendered, err := docgen.Render(genResult.Document, docgen.RenderOptions{Format: renderFormat, CitationStyle: style})
	if err != nil {
		return "", result, err
	}
	return rendered, result, nil
}

// wireMCPManuscriptGenerator registers the DocGen-backed generator with the MCP
// server. Called once from the CLI before the MCP stdio loop starts. This is
// the injection point that breaks the internal/wisdev ↔ internal/docgen import
// cycle: wisdev exposes the hook; cli (which can import both) wires it.
func wireMCPManuscriptGenerator() {
	internalwisdev.SetMCPManuscriptGenerator(func(ctx context.Context, opts internalwisdev.MCPManuscriptOptions) (string, internalwisdev.ManuscriptPipelineResult, error) {
		intent, err := docgen.ParseIntent(opts.Intent)
		if err != nil {
			return "", internalwisdev.ManuscriptPipelineResult{}, err
		}
		renderFormat, err := docgen.ParseRenderFormat(opts.Format)
		if err != nil {
			return "", internalwisdev.ManuscriptPipelineResult{}, err
		}
		style, err := citations.ParseStyle(opts.CitationStyle)
		if err != nil {
			return "", internalwisdev.ManuscriptPipelineResult{}, err
		}

		genResult, err := docgen.Generate(ctx, docgen.Options{
			Query:             opts.Query,
			Intent:            intent,
			CitationStyle:     style,
			Papers:            opts.Papers,
			PythonURL:         opts.PythonURL,
			VoiceInstructions: opts.Instructions,
			Manuscript: docgen.ManuscriptControls{
				TargetWords:  opts.TargetWords,
				MinCitations: opts.MinCitations,
				SectionFlow:  opts.SectionFlow,
				ReviewRounds: opts.ReviewRounds,
				Genre:        opts.Genre,
			},
			LLMClient: resolveResearchLLMClient(),
			JobID:     fmt.Sprintf("mcp_docgen_%d", time.Now().UnixMilli()),
		})
		if err != nil {
			return "", genResult.Pipeline, err
		}

		if renderFormat == docgen.FormatJSON && intent == docgen.IntentFullPaper {
			encoded, encErr := json.MarshalIndent(genResult.Pipeline, "", "  ")
			if encErr != nil {
				return "", genResult.Pipeline, encErr
			}
			return string(encoded), genResult.Pipeline, nil
		}
		rendered, err := docgen.Render(genResult.Document, docgen.RenderOptions{Format: renderFormat, CitationStyle: style})
		if err != nil {
			return "", genResult.Pipeline, err
		}
		return rendered, genResult.Pipeline, nil
	})
}

// ensureDocGenWired registers DocGen integrations (MCP tool + pkg/wisdev
// embedding API) exactly once per process. Safe to call from any CLI entrypoint.
func ensureDocGenWired() {
	docGenWireOnce.Do(func() {
		wireMCPManuscriptGenerator()
		wirePublicDocumentGenerator()
	})
}

var docGenWireOnce sync.Once

// wirePublicDocumentGenerator registers the pkg/wisdev.GenerateDocument backend.
func wirePublicDocumentGenerator() {
	agent.SetDocumentGenerator(func(ctx context.Context, opts agent.DocumentOptions) (agent.DocumentResult, error) {
		intent, err := docgen.ParseIntent(opts.Intent)
		if err != nil {
			return agent.DocumentResult{}, err
		}
		renderFormat, err := docgen.ParseRenderFormat(opts.Format)
		if err != nil {
			return agent.DocumentResult{}, err
		}
		style, err := citations.ParseStyle(opts.CitationStyle)
		if err != nil {
			return agent.DocumentResult{}, err
		}

		papers := docGenPapersFromResult(opts.Research)
		if len(papers) == 0 && len(opts.Papers) > 0 {
			for _, p := range opts.Papers {
				if strings.TrimSpace(p.Title) == "" {
					continue
				}
				papers = append(papers, search.Paper{
					ID: p.ID, Title: p.Title, Authors: p.Authors, Year: p.Year,
					Abstract: p.Abstract, Link: p.Link, DOI: p.DOI, Venue: p.Venue,
					CitationCount: p.CitationCount, OpenAccessUrl: p.OpenAccessURL,
				})
			}
		}

		genResult, err := docgen.Generate(ctx, docgen.Options{
			Query:             opts.Query,
			Intent:            intent,
			CitationStyle:     style,
			Papers:            papers,
			Research:          opts.Research,
			PythonURL:         opts.PythonURL,
			Offline:           opts.Offline,
			IncludeUncited:    opts.IncludeUncited,
			VoiceInstructions: opts.VoiceInstructions,
			ReviewStyle:       opts.ReviewStyle,
			Manuscript: docgen.ManuscriptControls{
				TargetWords:  opts.TargetWords,
				MinCitations: opts.MinCitations,
				SectionFlow:  opts.SectionFlow,
				ReviewRounds: opts.ReviewRounds,
				Genre:        opts.Genre,
			},
			LLMClient: resolveResearchLLMClient(),
			JobID:     fmt.Sprintf("docgen_%d", time.Now().UnixMilli()),
		})
		if err != nil {
			return agent.DocumentResult{}, err
		}

		out := agent.DocumentResult{}
		if docJSON, encErr := json.Marshal(genResult.Document); encErr != nil {
			return agent.DocumentResult{}, encErr
		} else {
			out.Document = docJSON
		}

		if renderFormat == docgen.FormatJSON && intent == docgen.IntentFullPaper {
			encoded, encErr := json.Marshal(genResult.Pipeline)
			if encErr != nil {
				return agent.DocumentResult{}, encErr
			}
			out.Pipeline = encoded
			out.Rendered = string(encoded)
			return out, nil
		}
		if renderFormat == docgen.FormatDOCX {
			data, _, expErr := docgen.ExportBytes(genResult.Document, docgen.RenderOptions{Format: docgen.FormatDOCX, CitationStyle: style})
			if expErr != nil {
				return agent.DocumentResult{}, expErr
			}
			out.Rendered = string(data)
			return out, nil
		}
		rendered, err := docgen.Render(genResult.Document, docgen.RenderOptions{Format: renderFormat, CitationStyle: style})
		if err != nil {
			return agent.DocumentResult{}, err
		}
		out.Rendered = rendered
		return out, nil
	})
}

// includeUncitedReferences reports whether the bibliography should list every
// retrieved source rather than only the in-text-cited ones. Corpus-file runs
// default to the full list (the corpus IS the subject under measurement); the
// --all-references flag forces it on for live retrieval too.
func includeUncitedReferences(opts docGenOptions) bool {
	return opts.allReferences || opts.corpusFile != ""
}

// preflightSidecar warns (loudly, on stderr) when section enrichment will silently
// degrade to scaffold prose because the Python sidecar is unreachable. It is a
// best-effort probe: any HTTP response means something is listening, so only a
// transport error triggers the warning. Offline runs skip the check entirely.
func preflightSidecar(stderr io.Writer, opts docGenOptions) {
	if opts.offline || stderr == nil {
		return
	}
	base := strings.TrimSpace(opts.pythonURL)
	if base == "" {
		base = internalwisdev.ResolvePythonBase()
	}
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err == nil {
		if resp, derr := http.DefaultClient.Do(req); derr == nil {
			resp.Body.Close()
			return // something is listening (any status code is fine)
		} else {
			err = derr
		}
	}
	note(stderr, "  ⚠ Python sidecar not reachable at %s (%v)", base, err)
	note(stderr, "    sections will use grounded scaffold prose, not fluent LLM prose.")
	note(stderr, "    start the sidecar (or pass --offline) for full-quality output.")
}

// resolveDocGenFormat picks the output format from the explicit --format flag,
// the -o file extension (.tex/.latex -> latex, .json -> json, .md -> markdown),
// or the legacy --json flag, defaulting to markdown.
func resolveDocGenFormat(format, output string, jsonOut bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "markdown", "md":
		return "markdown", nil
	case "latex", "tex":
		return "latex", nil
	case "html":
		return "html", nil
	case "docx":
		return "docx", nil
	case "json":
		return "json", nil
	case "":
		// fall through to inference
	default:
		return "", fmt.Errorf("unknown --format %q (want markdown, latex, html, docx, or json)", format)
	}
	if jsonOut {
		return "json", nil
	}
	switch {
	case hasSuffixFold(output, ".tex"), hasSuffixFold(output, ".latex"):
		return "latex", nil
	case hasSuffixFold(output, ".html"), hasSuffixFold(output, ".htm"):
		return "html", nil
	case hasSuffixFold(output, ".docx"):
		return "docx", nil
	case hasSuffixFold(output, ".json"):
		return "json", nil
	default:
		return "markdown", nil
	}
}

func hasSuffixFold(s, suffix string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(s)), suffix)
}

// absPathOrSelf resolves a path for display, falling back to the input when the
// working directory cannot be resolved.
func absPathOrSelf(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// smartDocGenMinCitations is the citation floor applied when the caller passes no
// --min-citations / --doc-min-citations. A grounded manuscript that cites only two
// or three sources reads thin; ten distinct sources is a sane review baseline that
// the retrieval loop can comfortably satisfy (maxUniquePapers default is 20–30).
// Explicit flags always win; corpus replays are exempt (the corpus IS the budget).
const smartDocGenMinCitations = 10

// docGenDefaultFilename derives a save path for a manuscript when the user did
// not pass an output flag: a slug of the first few query words plus a timestamp,
// with the extension matching the format (manuscript-llm-hallucinations-20260702-1504.md).
func docGenDefaultFilename(query, format string, now time.Time) string {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(words) > 6 {
		words = words[:6]
	}
	slug := strings.Join(words, "-")
	if slug == "" {
		slug = "untitled"
	}
	ext := ".md"
	switch format {
	case "latex":
		ext = ".tex"
	case "html":
		ext = ".html"
	case "docx":
		ext = ".docx"
	case "json":
		ext = ".json"
	}
	return fmt.Sprintf("manuscript-%s-%s%s", slug, now.Format("20060102-1504"), ext)
}

// docGenStageLabel maps a completed manuscript-pipeline stage to the label for
// the activity that runs NEXT, so the progress spinner always names what the
// pipeline is currently doing rather than what it just finished.
func docGenStageLabel(stage string) string {
	switch stage {
	case "build_raw_materials":
		return "Planning sections"
	case "plan_sections":
		return "Coordinating section ownership"
	case "coordination_plan":
		return "Composing visuals"
	case "compose_visuals":
		return "Drafting sections"
	case "write_sections":
		return "Verifying grounding"
	case "verify_blind.post_write":
		return "Refining sections"
	case "refine_sections", "verify_blind.post_refine":
		return "Rewriting abstract"
	case "regenerate_abstract", "dedupe_paragraphs", "verify_blind.post_dedupe":
		return "Fact-checking claims"
	case "fact_check.feed":
		return "Review round 1: revising flagged sections"
	case "review_revise.done", "coordinated_dedupe", "strip_self_methodology", "dedupe_sentences":
		return "Final cleanup"
	case "attach_uncited_specifics":
		return "Final fact-check"
	case "fact_check.score", "verify_blind.final":
		return "Compiling peer review"
	case "peer_review":
		return "Adversarial review"
	case "adversarial_review", "build_revision_tasks":
		return "Rendering manuscript"
	default:
		return ""
	}
}

// docGenProgressNotifier returns an OnStage callback for a manuscript pipeline
// that feeds friendly labels to a spinner update function, numbering the agentic
// review→revise rounds as they complete.
func docGenProgressNotifier(update func(string)) func(string) {
	round := 0
	return func(stage string) {
		if stage == "review_revise.round" {
			round++
			update(fmt.Sprintf("Review round %d done — re-reviewing", round))
			return
		}
		if label := docGenStageLabel(stage); label != "" {
			update(label)
		}
	}
}

// docGenRetrievePapers runs the local research loop and returns the retrieved
// papers (converted to the internal search model the manuscript pipeline
// consumes) alongside the raw YOLO result used for the reference list.
func docGenRetrievePapers(ctx context.Context, stderr io.Writer, opts docGenOptions) ([]search.Paper, *agent.YOLOResult, error) {
	llmClient := resolveResearchLLMClient()
	agentOpts := []agent.Option{agent.WithLLMClient(llmClient)}
	if opts.offline {
		agentOpts = append(agentOpts, agent.WithNoSearchProviders())
	} else if len(opts.providers) > 0 {
		agentOpts = append(agentOpts, agent.WithProviderNames(opts.providers...))
	}

	var research *agent.YOLOResult
	err := withQuietAgentLogs(func() error {
		return runWithProgress(stderr, "Retrieving grounded evidence", func() error {
			return withGlobalResearchLLMClient(llmClient, func() error {
				req := agent.YOLORequest{
					Task:                opts.query,
					OriginalQuery:       opts.query,
					Domain:              opts.domain,
					MaxIterations:       opts.maxIterations,
					MaxSearchTerms:      opts.maxSearchTerms,
					HitsPerSearch:       opts.hitsPerSearch,
					MaxUniquePapers:     opts.maxUniquePapers,
					DisableQueryEnhance: opts.disableEnhance,
				}
				if opts.showStages {
					req.OnProgress = func(event agent.ProgressEvent) {
						printCLIProgressEvent(stderr, event)
					}
				}
				var runErr error
				research, runErr = agent.NewAgent(agentOpts...).RunYOLO(ctx, req)
				return runErr
			})
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return docGenPapersFromResult(research), research, nil
}

// dumpCorpusPapers writes the retrieved papers to a JSON file so a later run can
// replay the identical corpus (live retrieval varies run-to-run, which otherwise
// makes prompt/pipeline A/B comparisons unreliable).
func dumpCorpusPapers(path string, papers []search.Paper) error {
	data, err := json.MarshalIndent(papers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// loadCorpusPapers reads a paper set written by dumpCorpusPapers for a deterministic,
// retrieval-free re-run.
func loadCorpusPapers(path string) ([]search.Paper, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var papers []search.Paper
	if err := json.Unmarshal(data, &papers); err != nil {
		return nil, fmt.Errorf("parse corpus JSON: %w", err)
	}
	return papers, nil
}

// docGenPapersFromResult converts the public YOLO papers into the internal
// search.Paper model the manuscript pipeline consumes for grounding.
func docGenPapersFromResult(research *agent.YOLOResult) []search.Paper {
	if research == nil || len(research.Papers) == 0 {
		return nil
	}
	out := make([]search.Paper, 0, len(research.Papers))
	for _, paper := range research.Papers {
		if strings.TrimSpace(paper.Title) == "" {
			continue
		}
		out = append(out, search.Paper{
			ID:                       paper.ID,
			Title:                    paper.Title,
			Abstract:                 paper.Abstract,
			Link:                     paper.Link,
			DOI:                      paper.DOI,
			ArxivID:                  paper.ArxivID,
			Source:                   paper.Source,
			SourceApis:               append([]string(nil), paper.SourceAPIs...),
			Authors:                  append([]string(nil), paper.Authors...),
			Year:                     paper.Year,
			Month:                    paper.Month,
			Venue:                    paper.Venue,
			Keywords:                 append([]string(nil), paper.Keywords...),
			CitationCount:            paper.CitationCount,
			ReferenceCount:           paper.ReferenceCount,
			InfluentialCitationCount: paper.InfluentialCitationCount,
			OpenAccessUrl:            paper.OpenAccessURL,
			PdfUrl:                   paper.PDFURL,
			Score:                    paper.Score,
			EvidenceLevel:            paper.EvidenceLevel,
			FullText:                 paper.FullText,
		})
	}
	return out
}

// renderManuscriptMarkdown turns a manuscript pipeline result into a readable
// Markdown document: ordered sections, visuals, the peer-review critique, and a
// reference list.
func renderManuscriptMarkdown(query string, research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult, includeUncited bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", strings.TrimSpace(query))
	b.WriteString("_Grounded manuscript draft generated by WisDev DocuGen._\n\n")

	draftBySection := make(map[string]internalwisdev.SectionDraftArtifact, len(result.SectionDrafts))
	for _, draft := range result.SectionDrafts {
		draftBySection[draft.SectionID] = draft
	}
	refEntries, packetRef := buildReferenceModel(research, result, includeUncited)
	doiRef := doiRefMap(refEntries)
	order := result.Blueprint.SectionOrder
	if len(order) == 0 {
		for _, draft := range result.SectionDrafts {
			order = append(order, draft.SectionID)
		}
	}
	for _, sectionID := range order {
		draft, ok := draftBySection[sectionID]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", firstNonEmpty(draft.Title, sectionID))
		content := strings.TrimSpace(remapDOICitations(remapSectionContentCitations(draft.Content, draft.ClaimPacketIDs, packetRef), doiRef))
		if content == "" {
			content = "_(no grounded content available for this section yet)_"
		}
		fmt.Fprintf(&b, "%s\n\n", content)
	}

	if len(result.VisualArtifacts) > 0 {
		b.WriteString("## Figures & Visuals\n\n")
		for i, visual := range result.VisualArtifacts {
			fmt.Fprintf(&b, "**Figure %d. %s**\n\n", i+1, firstNonEmpty(visual.Title, visual.Kind))
			if caption := strings.TrimSpace(visual.Caption); caption != "" {
				fmt.Fprintf(&b, "%s\n\n", caption)
			}
			switch spec := visual.Spec.(type) {
			case internalwisdev.ManuscriptTableSpec:
				b.WriteString(renderDocGenMarkdownTable(spec))
			case string:
				if strings.TrimSpace(spec) != "" {
					lang := ""
					if strings.EqualFold(visual.SpecType, "mermaid") {
						lang = "mermaid"
					}
					fmt.Fprintf(&b, "```%s\n%s\n```\n\n", lang, strings.TrimSpace(spec))
				}
			}
		}
	}

	b.WriteString(renderCritiqueMarkdown(result.CritiqueReport))

	if len(refEntries) > 0 {
		b.WriteString("## References\n\n")
		for i, ref := range refEntries {
			fmt.Fprintf(&b, "%d. %s\n", i+1, formatDocGenReferenceStruct(ref, false))
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderCritiqueMarkdown(critique map[string]any) string {
	if len(critique) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Peer Review\n\n")
	if score, ok := docGenFloat(critique["overallScore"]); ok {
		fmt.Fprintf(&b, "**Overall score:** %.2f\n\n", score)
	}
	if mode, ok := critique["verificationMode"].(string); ok && strings.TrimSpace(mode) != "" {
		fmt.Fprintf(&b, "_Verification: %s._\n\n", strings.TrimSpace(mode))
	}
	writeList := func(label, key string) {
		items := docGenStringSlice(critique[key])
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "**%s**\n\n", label)
		for _, item := range items {
			fmt.Fprintf(&b, "- %s\n", item)
		}
		b.WriteString("\n")
	}
	writeList("Strengths", "strengths")
	writeList("Weaknesses", "weaknesses")
	writeList("Risks", "risks")
	writeList("Recommendations", "recommendations")
	return b.String()
}

func renderDocGenMarkdownTable(spec internalwisdev.ManuscriptTableSpec) string {
	if len(spec.Headers) == 0 || len(spec.Rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| " + strings.Join(spec.Headers, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(spec.Headers)) + "\n")
	for _, row := range spec.Rows {
		cells := make([]string, len(spec.Headers))
		for i := range cells {
			if i < len(row) {
				cells[i] = strings.ReplaceAll(strings.TrimSpace(row[i]), "|", "\\|")
			}
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderDocGenLatexTable(spec internalwisdev.ManuscriptTableSpec) string {
	if len(spec.Headers) == 0 || len(spec.Rows) == 0 {
		return ""
	}
	// Bound every column (no bare `l`, which auto-widens to its content and pushes
	// later columns off the right margin) so the table fits within \textwidth.
	colspec := "p{0.24\\textwidth}"
	if len(spec.Headers) >= 2 {
		colspec += "p{0.44\\textwidth}"
	}
	if len(spec.Headers) >= 3 {
		colspec += "p{0.20\\textwidth}"
	}
	for i := 3; i < len(spec.Headers); i++ {
		colspec += "p{0.12\\textwidth}"
	}
	var b strings.Builder
	b.WriteString("\\begin{table}[h]\\centering\\footnotesize\n")
	b.WriteString("\\begin{tabular}{" + colspec + "}\n\\hline\n")
	hdr := make([]string, len(spec.Headers))
	for i, h := range spec.Headers {
		hdr[i] = "\\textbf{" + latexEscape(h) + "}"
	}
	b.WriteString(strings.Join(hdr, " & ") + " \\\\\n\\hline\n")
	for _, row := range spec.Rows {
		cells := make([]string, len(spec.Headers))
		for i := range cells {
			if i < len(row) {
				cells[i] = latexEscape(strings.TrimSpace(row[i]))
			}
		}
		b.WriteString(strings.Join(cells, " & ") + " \\\\\n")
	}
	b.WriteString("\\hline\n\\end{tabular}\n\\end{table}\n\n")
	return b.String()
}

func normalizeRefTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// buildCitationRefMap maps each claim-packet ID to its 1-based position in the
// rendered bibliography (the same order/filtering docGenSources uses), so the
// per-section positional [n] citations the sidecar emits can be rewritten to the
// global reference number. Returns nil when no mapping is possible.
func buildCitationRefMap(research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult) map[string]int {
	refByCanonicalID := map[string]int{}
	if research != nil && len(research.Papers) > 0 {
		// Bibliography is the retrieved papers (non-empty title) in order; map the
		// canonical sources onto them by normalized title.
		refByTitle := map[string]int{}
		ref := 0
		for _, paper := range research.Papers {
			if strings.TrimSpace(paper.Title) == "" {
				continue
			}
			ref++
			if key := normalizeRefTitle(paper.Title); key != "" {
				if _, exists := refByTitle[key]; !exists {
					refByTitle[key] = ref
				}
			}
		}
		for _, source := range result.RawMaterials.CanonicalSources {
			if n, ok := refByTitle[normalizeRefTitle(source.Title)]; ok {
				refByCanonicalID[source.CanonicalID] = n
			}
		}
	} else {
		ref := 0
		for _, source := range result.RawMaterials.CanonicalSources {
			if strings.TrimSpace(source.Title) == "" {
				continue
			}
			ref++
			refByCanonicalID[source.CanonicalID] = ref
		}
	}
	if len(refByCanonicalID) == 0 {
		return nil
	}
	packetRef := map[string]int{}
	for _, packet := range result.RawMaterials.ClaimPackets {
		for _, span := range packet.EvidenceSpans {
			if n, ok := refByCanonicalID[span.SourceCanonicalID]; ok {
				packetRef[packet.PacketID] = n
				break
			}
		}
	}
	if len(packetRef) == 0 {
		return nil
	}
	return packetRef
}

var citationMarkerPattern = regexp.MustCompile(`\[([0-9][0-9,;\s]*)\]`)

// remapSectionContentCitations rewrites a section's positional [n] markers (n =
// 1-based index into the section's ordered claim packets, the numbering the
// sidecar prompt used) into global bibliography reference numbers, so an in-text
// [n] resolves to reference [n]. A marker is rewritten only when every token in
// it resolves; otherwise it is left untouched.
func remapSectionContentCitations(content string, sectionPacketIDs []string, packetRef map[string]int) string {
	if content == "" || len(sectionPacketIDs) == 0 || len(packetRef) == 0 {
		return content
	}
	return citationMarkerPattern.ReplaceAllStringFunc(content, func(marker string) string {
		inner := strings.Trim(marker, "[]")
		refs := make([]string, 0, 4)
		seen := map[int]struct{}{}
		for _, tok := range strings.FieldsFunc(inner, func(r rune) bool {
			return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
		}) {
			k, err := strconv.Atoi(strings.TrimSpace(tok))
			if err != nil || k < 1 || k > len(sectionPacketIDs) {
				return marker
			}
			ref, ok := packetRef[sectionPacketIDs[k-1]]
			if !ok {
				return marker
			}
			if _, dup := seen[ref]; dup {
				continue
			}
			seen[ref] = struct{}{}
			refs = append(refs, strconv.Itoa(ref))
		}
		if len(refs) == 0 {
			return marker
		}
		return "[" + strings.Join(refs, ", ") + "]"
	})
}

// doiInTextPattern extracts a bare DOI from a link/URL/identifier.
var doiInTextPattern = regexp.MustCompile(`10\.\d{3,}/[^\s"'<>\])]+`)

// doiCitationBracketPattern matches a citation bracket the model filled with a raw DOI,
// DOI fragment, OR a comma/semicolon-separated LIST of DOIs instead of a numeric marker:
// "[10.1038/s41591-...]", "[1186/s12909]", "[10.3390/bioeng12010039, 10.21037/atm-23-87]".
var doiCitationBracketPattern = regexp.MustCompile(`\[\s*((?:10\.)?\d{3,}/[^\],;\s]+|s\d{4,}[^\],;\s]*)(?:\s*[,;]\s*(?:(?:10\.)?\d{3,}/[^\],;\s]+|s\d{4,}[^\],;\s]*))*\s*\]`)

// doiTokenSplitPattern splits the inner of a DOI-citation bracket into individual tokens.
var doiTokenSplitPattern = regexp.MustCompile(`\s*[,;]\s*`)

// whitespace-tidy patterns for prose left behind when a raw DOI marker is dropped.
var multiSpacePattern = regexp.MustCompile(`[ \t]{2,}`)
var spaceBeforePunctPattern = regexp.MustCompile(`\s+([.,;:)])`)

func normalizeDOIKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if m := doiInTextPattern.FindString(s); m != "" {
		s = m
	}
	return strings.TrimRight(s, ".,;)")
}

// doiRefMap maps each reference's DOI to its 1-based number, so raw [DOI] citations can
// be rewritten to the numeric marker.
func doiRefMap(refs []docGenReference) map[string]int {
	out := make(map[string]int, len(refs))
	for i, ref := range refs {
		if key := normalizeDOIKey(ref.link); strings.HasPrefix(key, "10.") {
			if _, exists := out[key]; !exists {
				out[key] = i + 1
			}
		}
	}
	return out
}

// remapDOICitations rewrites in-text [DOI] markers to the numeric reference number when
// the DOI resolves, and DROPS the marker otherwise — a raw DOI fragment leaking into the
// prose (the model citing by identifier rather than the bracketed number) is noise, not a
// usable citation.
func remapDOICitations(content string, doiRef map[string]int) string {
	if content == "" {
		return content
	}
	if !doiCitationBracketPattern.MatchString(content) {
		return content
	}
	out := doiCitationBracketPattern.ReplaceAllStringFunc(content, func(marker string) string {
		inner := strings.Trim(strings.TrimSpace(marker), "[]")
		nums := make([]string, 0, 2)
		seen := map[int]struct{}{}
		for _, tok := range doiTokenSplitPattern.Split(inner, -1) {
			key := normalizeDOIKey(tok)
			if !strings.HasPrefix(key, "10.") {
				key = "10." + strings.TrimPrefix(key, "10.")
			}
			if n, ok := doiRef[key]; ok {
				if _, dup := seen[n]; !dup {
					seen[n] = struct{}{}
					nums = append(nums, strconv.Itoa(n))
				}
			}
		}
		if len(nums) == 0 {
			return "" // none of the grouped DOIs resolve -> drop the noise
		}
		return "[" + strings.Join(nums, ", ") + "]"
	})
	// Tidy the gap a dropped marker leaves ("fragment  leaks" / "fragment .").
	out = multiSpacePattern.ReplaceAllString(out, " ")
	out = spaceBeforePunctPattern.ReplaceAllString(out, "$1")
	return out
}

// docGenReference is a structured bibliography entry (decoupled from formatting)
// so references can be deduped (preprint vs published), pruned to only-cited, and
// reordered by first citation before rendering.
type docGenReference struct {
	titleKey  string
	authors   []string
	year      int
	title     string
	venue     string
	link      string
	citations int
	preprint  bool
}

// collectDocGenReferences builds the structured reference list in retrieval order
// (papers preferred; canonical sources as fallback) — the same order/selection
// buildCitationRefMap numbers against.
func collectDocGenReferences(research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult) []docGenReference {
	out := make([]docGenReference, 0)
	if research != nil && len(research.Papers) > 0 {
		for _, paper := range research.Papers {
			if strings.TrimSpace(paper.Title) == "" {
				continue
			}
			link := firstNonEmpty(paper.Link, paper.OpenAccessURL, paper.DOI)
			out = append(out, docGenReference{
				titleKey: normalizeRefTitle(paper.Title), authors: paper.Authors, year: paper.Year,
				title: paper.Title, venue: paper.Venue, link: link, citations: paper.CitationCount,
				preprint: isPreprintReference(paper.Venue, link),
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	for _, source := range result.RawMaterials.CanonicalSources {
		if strings.TrimSpace(source.Title) == "" {
			continue
		}
		// Carry author/venue from the canonical record (corpus-file mode has no
		// research.Papers, so this is the only branch that runs there). Dropping them
		// produced author-less references and let mis-resolved titles ("A. L. GURSON",
		// "Pergamon") surface undisambiguated.
		out = append(out, docGenReference{
			titleKey: normalizeRefTitle(source.Title), authors: source.Authors, year: source.Year,
			title: source.Title, venue: source.Venue, link: source.LandingURL,
			preprint: isPreprintReference(source.Venue, source.LandingURL),
		})
	}
	return out
}

func isPreprintReference(venue, link string) bool {
	hay := strings.ToLower(venue + " " + link)
	for _, marker := range []string{"preprint", "arxiv", "biorxiv", "medrxiv", "ssrn", "research square", "researchsquare", "preprints.org"} {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

func formatDocGenReferenceStruct(ref docGenReference, latex bool) string {
	if latex {
		return formatDocGenReferenceLatex(ref.authors, ref.year, ref.title, ref.venue, ref.link, ref.citations)
	}
	return formatDocGenReference(ref.authors, ref.year, ref.title, ref.venue, ref.link, ref.citations)
}

// buildReferenceModel produces the final bibliography — deduplicated across
// preprint/published versions, ordered by first citation, and (by default) pruned
// to only in-text-cited sources; when includeUncited is set (corpus-file mode or
// --all-references) the uncited retrieved sources are appended after the cited ones
// so the whole corpus is represented — together with a packetID -> final reference number map for
// remapSectionContentCitations. When no in-text citation resolves (e.g. scaffold
// output or no packets), it falls back to every retrieved reference in retrieval
// order with the base numbering.
func buildReferenceModel(research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult, includeUncited bool) ([]docGenReference, map[string]int) {
	refs := collectDocGenReferences(research, result)
	baseRef := buildCitationRefMap(research, result) // packetID -> 1-based base index
	if len(refs) == 0 || len(baseRef) == 0 {
		return refs, baseRef
	}
	citedOrder := citedBaseRefOrder(result, baseRef)
	if len(citedOrder) == 0 {
		return refs, baseRef
	}
	final, baseToFinal := refineCitedReferences(refs, citedOrder)
	packetRef := make(map[string]int, len(baseRef))
	for packetID, base := range baseRef {
		if n, ok := baseToFinal[base]; ok {
			packetRef[packetID] = n
		}
	}
	if includeUncited {
		// Keep the cited-first ordering, then append every retrieved source the model
		// did not cite (deduped by normalized title) so the bibliography represents the
		// full corpus rather than only the subset the model happened to reference.
		final = appendUncitedReferences(final, refs)
	}
	return final, packetRef
}

// appendUncitedReferences appends references from allRefs not already present in
// final (matched by normalized title), preserving order. Title-less references
// (un-dedupable) are always included.
func appendUncitedReferences(final, allRefs []docGenReference) []docGenReference {
	present := make(map[string]struct{}, len(final))
	for _, ref := range final {
		if ref.titleKey != "" {
			present[ref.titleKey] = struct{}{}
		}
	}
	for _, ref := range allRefs {
		if ref.titleKey == "" {
			final = append(final, ref)
			continue
		}
		if _, ok := present[ref.titleKey]; ok {
			continue
		}
		present[ref.titleKey] = struct{}{}
		final = append(final, ref)
	}
	return final
}

// citedBaseRefOrder walks the drafted sections in blueprint order and returns the
// base reference indices in first in-text-citation appearance order. A section's
// local [k] marker maps to its k-th claim packet -> base ref index.
func citedBaseRefOrder(result internalwisdev.ManuscriptPipelineResult, baseRef map[string]int) []int {
	draftBySection := make(map[string]internalwisdev.SectionDraftArtifact, len(result.SectionDrafts))
	for _, d := range result.SectionDrafts {
		draftBySection[d.SectionID] = d
	}
	order := result.Blueprint.SectionOrder
	if len(order) == 0 {
		for _, d := range result.SectionDrafts {
			order = append(order, d.SectionID)
		}
	}
	seen := map[int]struct{}{}
	out := make([]int, 0)
	for _, sectionID := range order {
		draft, ok := draftBySection[sectionID]
		if !ok {
			continue
		}
		for _, match := range citationMarkerPattern.FindAllStringSubmatch(draft.Content, -1) {
			for _, tok := range strings.FieldsFunc(match[1], func(r rune) bool {
				return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
			}) {
				k, err := strconv.Atoi(strings.TrimSpace(tok))
				if err != nil || k < 1 || k > len(draft.ClaimPacketIDs) {
					continue
				}
				base, ok := baseRef[draft.ClaimPacketIDs[k-1]]
				if !ok {
					continue
				}
				if _, dup := seen[base]; dup {
					continue
				}
				seen[base] = struct{}{}
				out = append(out, base)
			}
		}
	}
	return out
}

// refineCitedReferences dedups the cited references by normalized title (merging
// preprint/published versions, preferring the published/DOI one), keeps only the
// cited ones, orders them by first citation, and returns the final list plus a
// base-index -> final-number map (every base index sharing a title with a kept
// reference maps to that reference's final number).
func refineCitedReferences(refs []docGenReference, citedOrder []int) ([]docGenReference, map[int]int) {
	titleKeyFor := func(base int) string {
		key := refs[base-1].titleKey
		if key == "" {
			return "ref:" + strconv.Itoa(base) // un-dedupable; treat as unique
		}
		return key
	}
	finalByTitle := map[string]int{} // titleKey -> 1-based position in final
	repByTitle := map[string]int{}   // titleKey -> chosen representative base index
	final := make([]docGenReference, 0, len(citedOrder))
	for _, base := range citedOrder {
		if base < 1 || base > len(refs) {
			continue
		}
		key := titleKeyFor(base)
		if pos, exists := finalByTitle[key]; exists {
			if shouldPreferReference(refs[base-1], refs[repByTitle[key]-1]) {
				final[pos-1] = refs[base-1]
				repByTitle[key] = base
			}
			continue
		}
		final = append(final, refs[base-1])
		finalByTitle[key] = len(final)
		repByTitle[key] = base
	}
	baseToFinal := map[int]int{}
	for i := range refs {
		base := i + 1
		if n, ok := finalByTitle[titleKeyFor(base)]; ok {
			baseToFinal[base] = n
		}
	}
	return final, baseToFinal
}

// shouldPreferReference prefers a published version over a preprint, then one with
// a DOI link, when two references denote the same work.
func shouldPreferReference(candidate, current docGenReference) bool {
	if current.preprint && !candidate.preprint {
		return true
	}
	if current.preprint == candidate.preprint {
		candDOI := strings.Contains(strings.ToLower(candidate.link), "doi.org")
		curDOI := strings.Contains(strings.ToLower(current.link), "doi.org")
		return candDOI && !curDOI
	}
	return false
}

// docGenSources renders the full structured reference list (used as a fallback and
// by tests); the renderers use buildReferenceModel for the cited/deduped list.
func docGenSources(research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult) []string {
	refs := collectDocGenReferences(research, result)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, formatDocGenReferenceStruct(ref, false))
	}
	return out
}

func formatDocGenReference(authors []string, year int, title, venue, link string, citations int) string {
	var b strings.Builder
	if authorLabel := docGenAuthorLabel(authors); authorLabel != "" {
		b.WriteString(authorLabel)
		b.WriteString(" ")
	}
	if year > 0 {
		fmt.Fprintf(&b, "(%d). ", year)
	}
	b.WriteString(strings.TrimSpace(title))
	if !strings.HasSuffix(strings.TrimSpace(title), ".") {
		b.WriteString(".")
	}
	if venue = strings.TrimSpace(venue); venue != "" {
		fmt.Fprintf(&b, " *%s*.", venue)
	}
	if citations > 0 {
		fmt.Fprintf(&b, " %d citations.", citations)
	}
	if link = strings.TrimSpace(link); link != "" {
		fmt.Fprintf(&b, " %s", link)
	}
	return strings.TrimSpace(b.String())
}

func docGenAuthorLabel(authors []string) string {
	cleaned := make([]string, 0, len(authors))
	for _, author := range authors {
		if trimmed := strings.TrimSpace(author); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	switch len(cleaned) {
	case 0:
		return ""
	case 1:
		return cleaned[0]
	case 2:
		return cleaned[0] + " & " + cleaned[1]
	default:
		return cleaned[0] + " et al."
	}
}

// renderManuscriptLatex renders the manuscript as a self-contained, compilable
// LaTeX article: title, abstract environment, ordered \section bodies, a figures
// block, the peer-review notes, and a thebibliography reference list.
func renderManuscriptLatex(query string, research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult, includeUncited bool) string {
	var b strings.Builder
	b.WriteString("\\documentclass[11pt]{article}\n")
	b.WriteString("\\usepackage[utf8]{inputenc}\n")
	b.WriteString("\\usepackage[T1]{fontenc}\n")
	b.WriteString("\\usepackage{lmodern}\n") // vector T1 fonts; avoids slow bitmap (ec) font generation
	b.WriteString("\\usepackage[margin=1in]{geometry}\n")
	b.WriteString("\\usepackage{graphicx}\n")
	b.WriteString("\\usepackage{amsmath}\n")
	b.WriteString("\\usepackage{enumitem}\n")
	b.WriteString("\\usepackage{hyperref}\n")
	b.WriteString("\\hypersetup{colorlinks=true, urlcolor=blue, linkcolor=black, citecolor=black}\n\n")
	fmt.Fprintf(&b, "\\title{%s}\n", latexEscape(strings.TrimSpace(query)))
	b.WriteString("\\author{Generated by WisDev DocuGen}\n")
	b.WriteString("\\date{\\today}\n\n")
	b.WriteString("\\begin{document}\n\\maketitle\n\n")

	draftBySection := make(map[string]internalwisdev.SectionDraftArtifact, len(result.SectionDrafts))
	for _, draft := range result.SectionDrafts {
		draftBySection[draft.SectionID] = draft
	}
	refEntries, packetRef := buildReferenceModel(research, result, includeUncited)
	doiRef := doiRefMap(refEntries)
	order := result.Blueprint.SectionOrder
	if len(order) == 0 {
		for _, draft := range result.SectionDrafts {
			order = append(order, draft.SectionID)
		}
	}

	if abstract, ok := draftBySection["abstract"]; ok {
		if content := strings.TrimSpace(remapDOICitations(remapSectionContentCitations(abstract.Content, abstract.ClaimPacketIDs, packetRef), doiRef)); content != "" {
			b.WriteString("\\begin{abstract}\n")
			b.WriteString(latexParagraphs(content))
			b.WriteString("\n\\end{abstract}\n\n")
		}
	}

	for _, sectionID := range order {
		if sectionID == "abstract" {
			continue
		}
		draft, ok := draftBySection[sectionID]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\\section{%s}\n", latexEscape(firstNonEmpty(draft.Title, sectionID)))
		content := strings.TrimSpace(remapDOICitations(remapSectionContentCitations(draft.Content, draft.ClaimPacketIDs, packetRef), doiRef))
		if content == "" {
			b.WriteString("\\emph{(no grounded content available for this section yet)}\n\n")
			continue
		}
		b.WriteString(latexParagraphs(content))
		b.WriteString("\n\n")
	}

	if len(result.VisualArtifacts) > 0 {
		b.WriteString("\\section*{Figures \\& Visuals}\n")
		for i, visual := range result.VisualArtifacts {
			fmt.Fprintf(&b, "\\paragraph{Figure %d. %s}\n", i+1, latexEscape(firstNonEmpty(visual.Title, visual.Kind)))
			if caption := strings.TrimSpace(visual.Caption); caption != "" {
				b.WriteString(latexEscape(caption) + "\n\n")
			}
			switch spec := visual.Spec.(type) {
			case internalwisdev.ManuscriptTableSpec:
				b.WriteString(renderDocGenLatexTable(spec))
			case string:
				if strings.TrimSpace(spec) != "" {
					// Mermaid can't be typeset by pdflatex, so present the spec honestly
					// as the diagram's source at a small size to keep long node lines
					// from overflowing the text margin.
					b.WriteString("\\par\\noindent\\textit{Diagram source (Mermaid):}\n")
					b.WriteString("{\\footnotesize\n\\begin{verbatim}\n")
					b.WriteString(strings.TrimSpace(spec) + "\n")
					b.WriteString("\\end{verbatim}\n}\n\n")
				}
			}
		}
	}

	b.WriteString(renderCritiqueLatex(result.CritiqueReport))

	if len(refEntries) > 0 {
		b.WriteString("\\begin{thebibliography}{99}\n")
		for i, ref := range refEntries {
			fmt.Fprintf(&b, "\\bibitem{ref%d} %s\n", i+1, formatDocGenReferenceStruct(ref, true))
		}
		b.WriteString("\\end{thebibliography}\n\n")
	}

	b.WriteString("\\end{document}\n")
	return b.String()
}

func renderCritiqueLatex(critique map[string]any) string {
	if len(critique) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\\section*{Peer Review}\n")
	if score, ok := docGenFloat(critique["overallScore"]); ok {
		fmt.Fprintf(&b, "\\textbf{Overall score:} %.2f\n\n", score)
	}
	if mode, ok := critique["verificationMode"].(string); ok && strings.TrimSpace(mode) != "" {
		fmt.Fprintf(&b, "\\textit{Verification: %s.}\n\n", latexEscape(strings.TrimSpace(mode)))
	}
	writeList := func(label, key string) {
		items := docGenStringSlice(critique[key])
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\\textbf{%s}\n", latexEscape(label))
		b.WriteString("\\begin{itemize}[leftmargin=1.5em]\n")
		for _, item := range items {
			fmt.Fprintf(&b, "  \\item %s\n", latexEscape(item))
		}
		b.WriteString("\\end{itemize}\n\n")
	}
	writeList("Strengths", "strengths")
	writeList("Weaknesses", "weaknesses")
	writeList("Risks", "risks")
	writeList("Recommendations", "recommendations")
	return b.String()
}

func formatDocGenReferenceLatex(authors []string, year int, title, venue, link string, citations int) string {
	var b strings.Builder
	if label := docGenAuthorLabel(authors); label != "" {
		b.WriteString(latexEscape(label) + " ")
	}
	if year > 0 {
		fmt.Fprintf(&b, "(%d). ", year)
	}
	title = strings.TrimSpace(title)
	b.WriteString(latexEscape(title))
	if !strings.HasSuffix(title, ".") {
		b.WriteString(".")
	}
	if venue = strings.TrimSpace(venue); venue != "" {
		fmt.Fprintf(&b, " \\emph{%s}.", latexEscape(venue))
	}
	if citations > 0 {
		fmt.Fprintf(&b, " %d citations.", citations)
	}
	if link = strings.TrimSpace(link); link != "" {
		fmt.Fprintf(&b, " \\url{%s}", link)
	}
	return strings.TrimSpace(b.String())
}

var latexParagraphSplit = regexp.MustCompile(`\n\s*\n+`)

func latexParagraphs(content string) string {
	parts := latexParagraphSplit.Split(strings.TrimSpace(content), -1)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, latexEscape(part))
		}
	}
	return strings.Join(out, "\n\n")
}

// latexEscape decodes common HTML entities and Unicode typography that leak from
// source abstracts, then escapes LaTeX special characters so the text compiles
// under pdflatex regardless of inputenc support.
func latexEscape(s string) string {
	s = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'", "&apos;", "'",
	).Replace(s)
	s = strings.NewReplacer(
		"\u2014", "---", // em dash
		"\u2013", "--", // en dash
		"\u2018", "`", "\u2019", "'", // curly single quotes
		"\u201c", "``", "\u201d", "''", // curly double quotes
		"\u2026", "...", // ellipsis
		"\u00a0", " ", "\u2009", " ", "\u202f", " ", // nbsp / thin / narrow-nbsp
		"\u2212", "-", // minus sign
	).Replace(s)
	var b strings.Builder
	for _, ch := range s {
		switch ch {
		case '\\':
			b.WriteString("\\textbackslash{}")
		case '&':
			b.WriteString("\\&")
		case '%':
			b.WriteString("\\%")
		case '$':
			b.WriteString("\\$")
		case '#':
			b.WriteString("\\#")
		case '_':
			b.WriteString("\\_")
		case '{':
			b.WriteString("\\{")
		case '}':
			b.WriteString("\\}")
		case '~':
			b.WriteString("\\textasciitilde{}")
		case '^':
			b.WriteString("\\textasciicircum{}")
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func printDocGenSummary(stderr io.Writer, result internalwisdev.ManuscriptPipelineResult) {
	fmt.Fprintln(stderr)
	printSection(stderr, "DocuGen summary")
	fmt.Fprintf(stderr, "  %s sections drafted: %d\n", statusGlyph("ok"), len(result.SectionDrafts))
	fmt.Fprintf(stderr, "  %s visuals composed: %d\n", statusGlyph("ok"), len(result.VisualArtifacts))
	fmt.Fprintf(stderr, "  %s claim packets: %d\n", statusGlyph("ok"), len(result.RawMaterials.ClaimPackets))
	fmt.Fprintf(stderr, "  %s sources grounded: %d\n", statusGlyph("ok"), len(result.RawMaterials.CanonicalSources))
	if score, ok := docGenFloat(result.CritiqueReport["overallScore"]); ok {
		fmt.Fprintf(stderr, "  %s peer-review score: %.2f\n", statusGlyph("ok"), score)
	}
	pending := 0
	for _, task := range result.RevisionTasks {
		if status, _ := task["status"].(string); status == "pending" {
			pending++
		}
	}
	if pending > 0 {
		fmt.Fprintf(stderr, "  %s open revision tasks: %d\n", statusGlyph("warn"), pending)
	}
	fmt.Fprintln(stderr)
	printScholarLMBrandingProminent(stderr)
}

func docGenStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

func docGenFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
