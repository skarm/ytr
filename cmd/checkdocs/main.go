// Command checkdocs verifies the complete documentation inventory. -online
// additionally checks the current public index and document hashes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/skarm/ytr/internal/apidoc"
)

func main() {
	online := flag.Bool("online", false, "also check the current public documentation")
	report := flag.String("report", "", "write an actionable source diff and candidate snapshot to this directory (requires -online)")
	snapshot := flag.String("snapshot", "", "rebuild source facts from downloaded Markdown in this directory; reviewed hashes must match")
	accept := flag.String("accept", "", "validate a report candidate in an isolated build, then accept its source facts and generated bindings")

	flag.Parse()

	if err := execute(*online, *report, *snapshot, *accept); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(online bool, report, snapshot, accept string) error {
	if online && report == "" {
		report = "api-review"
	}

	if (report != "" && !online) || (snapshot != "" && (online || accept != "")) || (accept != "" && (online || report != "")) {
		return errors.New("checkdocs: incompatible flags; -report requires -online, -snapshot and -accept run separately")
	}

	if accept != "" {
		return acceptReview(context.Background(), accept)
	}

	if snapshot != "" {
		catalog, sources, err := readInventory()
		if err != nil {
			return err
		}

		documents, err := apidoc.LocalDocuments(context.Background(), sources, snapshot)
		if err != nil {
			return err
		}

		documents.Revision = catalog.Revision
		if err := apidoc.CheckDocuments(documents, catalog, sources); err != nil {
			return err
		}

		return apidoc.WriteJSON("api/documentation.json", documents)
	}

	if report == "" {
		return check(online)
	}

	catalog, sources, err := readInventory()
	if err != nil {
		return err
	}

	var baseline apidoc.Documents
	if err := apidoc.ReadJSON("api/documentation.json", &baseline); err != nil {
		return err
	}

	if err := apidoc.CheckDocuments(baseline, catalog, sources); err != nil {
		return err
	}

	models, err := apidoc.LoadModels("api/models.json", catalog)
	if err != nil {
		return err
	}

	review, candidate, index, err := apidoc.ReviewOnline(context.Background(), catalog, baseline, models, apidoc.HTTPFetcher(&http.Client{Timeout: 30 * time.Second}))
	if err != nil {
		return err
	}

	if err := apidoc.WriteReview(report, review, candidate, index); err != nil {
		return err
	}

	fmt.Printf("Documentation review: %s (%d changes)\n", filepath.Join(report, "report.txt"), len(review.Changes))

	if len(review.Changes) > 0 {
		return fmt.Errorf("documentation changed: %d changes; inspect %s", len(review.Changes), filepath.Join(report, "report.txt"))
	}

	return nil
}

func check(online bool) error {
	if online {
		return execute(true, "api-review", "", "")
	}

	catalog, sources, err := readInventory()
	if err != nil {
		return err
	}

	models, err := apidoc.LoadModels("api/models.json", catalog)
	if err != nil {
		return err
	}

	var documents apidoc.Documents
	if err := apidoc.ReadJSON("api/documentation.json", &documents); err != nil {
		return err
	}

	if err := apidoc.CheckDocuments(documents, catalog, sources); err != nil {
		return err
	}

	if err := apidoc.CheckModelSections(documents, models); err != nil {
		return err
	}

	fmt.Printf("Tracker docs %s (%s): %d pages, %d operations; inventory verified\n", catalog.Revision, catalog.VerifiedOn, len(sources.Pages), len(catalog.Operations))

	return nil
}

func readInventory() (apidoc.Catalog, apidoc.Sources, error) {
	catalog, err := apidoc.Load("api/catalog.json")
	if err != nil {
		return catalog, apidoc.Sources{}, err
	}

	var sources apidoc.Sources
	if err := apidoc.ReadJSON("api/sources.json", &sources); err != nil {
		return catalog, sources, err
	}

	index, err := os.ReadFile("api/index.txt")
	if err != nil {
		return catalog, sources, err
	}

	if err := apidoc.Check(catalog, sources, string(index)); err != nil {
		return catalog, sources, err
	}

	return catalog, sources, nil
}
