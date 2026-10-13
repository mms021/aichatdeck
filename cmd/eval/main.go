// Command eval runs the platform's evals and writes an immutable run directory.
//
//	eval verifier -judge claude -model claude-opus-5 -reps 3
//	eval verifier -judge oracle            # harness check: must score 100%
//	eval retrieval                         # needs DATABASE_URL (Postgres)
//
// Set LANGFUSE_HOST, LANGFUSE_PUBLIC_KEY and LANGFUSE_SECRET_KEY to also
// export the run to Langfuse as an experiment.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/db"
	"aichatdeck/internal/evals"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: eval verifier|retrieval [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "verifier":
		verifier(os.Args[2:])
	case "retrieval":
		retrieval(os.Args[2:])
	default:
		log.Fatalf("unknown eval %q", os.Args[1])
	}
}

func verifier(args []string) {
	fs := flag.NewFlagSet("verifier", flag.ExitOnError)
	casesPath := fs.String("cases", "evals/verifier/cases.jsonl", "case set")
	judgeName := fs.String("judge", "oracle", "claude | oracle | echo | always-verified | always-rejected | always-inconclusive")
	modelID := fs.String("model", agent.NewClaudeBrain("").Model, "Claude model for -judge claude")
	reps := fs.Int("reps", 3, "repetitions per case")
	parallel := fs.Int("parallel", 4, "concurrent cases")
	out := fs.String("out", "evals/runs", "runs root")
	dryRun := fs.Bool("dry-run", false, "print the plan and cost estimate; call nothing")
	_ = fs.Parse(args)

	cases, err := evals.LoadVerifierCases(*casesPath)
	if err != nil {
		log.Fatal(err)
	}
	var judge evals.Judge
	wantModel := ""
	switch *judgeName {
	case "claude":
		brain := agent.NewClaudeBrain(os.Getenv("ANTHROPIC_API_KEY"))
		brain.Model = *modelID
		judge, wantModel = evals.ClaudeJudge{Brain: brain}, *modelID
	case "oracle":
		judge = evals.OracleJudge{Cases: cases}
	case "echo":
		judge = evals.BrainJudge{Label: "echo", Brain: agent.EchoBrain{}}
	case "always-verified", "always-rejected", "always-inconclusive":
		judge = evals.ConstantJudge{Verdict: (*judgeName)[len("always-"):]}
	default:
		log.Fatalf("unknown judge %q", *judgeName)
	}
	calls := len(cases) * *reps
	if *dryRun {
		// ~6k input (system prompt + case) and ~1.5k output tokens per call.
		est := evals.Cost(*modelID, 6000, 1500, 0, 0) * float64(calls)
		fmt.Printf("judge=%s cases=%d reps=%d calls=%d", judge.Name(), len(cases), *reps, calls)
		if *judgeName == "claude" {
			fmt.Printf(" estimated cost ≈ $%.2f (before prompt caching)", est)
		}
		fmt.Println()
		return
	}

	runID := evals.NewRunID("verifier-" + judge.Name())
	run, err := evals.CreateRun(*out, runID)
	if err != nil {
		log.Fatal(err)
	}
	must(run.WriteJSON("config.json", map[string]any{"eval": "verifier", "judge": judge.Name(), "cases_file": *casesPath,
		"cases": len(cases), "reps": *reps, "started_at": time.Now().UTC()}))
	log.Printf("run %s: %d calls", runID, calls)
	ctx := context.Background()
	rows, errs := evals.RunVerifier(ctx, judge, cases, evals.VerifierConfig{Reps: *reps, Parallel: *parallel, WantModel: wantModel})
	summary := evals.SummarizeVerifier(runID, judge.Name(), cases, *reps, rows, errs)
	must(evals.WriteJSONL(run, "results.jsonl", rows))
	must(evals.WriteJSONL(run, "errors.jsonl", errs))
	must(run.WriteJSON("summary.json", summary))
	must(run.WriteText("report.md", summary.Markdown(rows, errs)))
	fmt.Printf("%s\naccuracy %s\nfalse-verified %s\nreport: %s/report.md\n", runID, summary.Accuracy, summary.FalseVerified, run.Dir)

	exportLangfuse(ctx, evals.Experiment{ID: runID, Name: "verifier/" + judge.Name(),
		Description: "Verifier verdicts vs author-written labels", DatasetID: "aichatdeck-verifier-cases"},
		evals.VerifierExportItems(cases, rows))
}

func retrieval(args []string) {
	fs := flag.NewFlagSet("retrieval", flag.ExitOnError)
	corpusPath := fs.String("corpus", "evals/retrieval/corpus.jsonl", "memory entries")
	queriesPath := fs.String("queries", "evals/retrieval/queries.jsonl", "queries with relevant entries")
	label := fs.String("label", "memory-search", "name for this configuration")
	out := fs.String("out", "evals/runs", "runs root")
	_ = fs.Parse(args)

	corpus, queries, err := evals.LoadRetrieval(*corpusPath, *queriesPath)
	if err != nil {
		log.Fatal(err)
	}
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		base = "postgres://aichatdeck:aichatdeck@localhost:47432/aichatdeck?sslmode=disable"
	}
	url, drop, err := db.ScratchSchema(base, "eval")
	if err != nil {
		log.Fatal(err)
	}
	defer drop()
	d, err := db.Open(url)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	started := time.Now().UTC()
	rows, err := evals.RunRetrieval(ctx, d, corpus, queries)
	if err != nil {
		log.Fatal(err)
	}
	runID := evals.NewRunID("retrieval-" + *label)
	run, err := evals.CreateRun(*out, runID)
	if err != nil {
		log.Fatal(err)
	}
	summary := evals.SummarizeRetrieval(runID, *label, len(corpus), rows)
	must(run.WriteJSON("config.json", map[string]any{"eval": "retrieval", "label": *label, "corpus": *corpusPath,
		"queries": *queriesPath, "started_at": started}))
	must(evals.WriteJSONL(run, "results.jsonl", rows))
	must(run.WriteJSON("summary.json", summary))
	must(run.WriteText("report.md", summary.Markdown(rows)))
	fmt.Printf("%s\nrecall@5 %s\nMRR %s\nreport: %s/report.md\n", runID, summary.Recall5, summary.MRR, run.Dir)

	exportLangfuse(ctx, evals.Experiment{ID: runID, Name: "retrieval/" + *label,
		Description: "Memory search ranking vs author-written relevance", DatasetID: "aichatdeck-retrieval-queries"},
		evals.RetrievalExportItems(rows, started))
}

func exportLangfuse(ctx context.Context, exp evals.Experiment, items []evals.ExportItem) {
	lf := evals.Langfuse{Host: os.Getenv("LANGFUSE_HOST"), PublicKey: os.Getenv("LANGFUSE_PUBLIC_KEY"), SecretKey: os.Getenv("LANGFUSE_SECRET_KEY")}
	if lf.Host == "" || lf.PublicKey == "" || lf.SecretKey == "" {
		return
	}
	if err := lf.Export(ctx, exp, items); err != nil {
		log.Fatalf("langfuse: %v", err)
	}
	fmt.Printf("exported %d items to Langfuse experiment %s\n", len(items), exp.ID)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
