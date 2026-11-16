package cmd

import (
	"fmt"
	"os"

	"garfield/internal/suggeststore"
)

// runDisplay prints the suggestions stored under the output directory.
//
// It never walks the acquisition tree, measures a frame or writes anything: the
// suggestion record is a complete description of what a --suggest run decided and
// what it would reject, which is why --display can run with the data volume
// unmounted. A record written before --display existed lacks that description;
// it is reported rather than rendered as a table of zeros.
func runDisplay(opts analyzeOptions) error {
	if opts.output == "" {
		return fmt.Errorf("--display requires --output: suggestions are stored under %s", suggestionPath("<output>"))
	}
	dir := opts.output
	if opts.target != "" {
		dir = targetMetricsDir(opts.output, opts.target)
	}
	path := suggestionPath(dir)

	store, err := suggeststore.Open(path)
	if err != nil {
		return err
	}
	// Open reads the file and closes it; a read-only command has nothing to
	// flush, and Close could compact (rewrite) the store.

	if opts.filter != "" {
		rec, ok := store.Lookup(opts.filter)
		if !ok {
			return fmt.Errorf("no stored suggestion for filter %s in %q; run `garfield analyze --suggest` first",
				opts.filter, path)
		}
		if rec.Display == nil {
			return fmt.Errorf("the stored suggestion for %s in %q predates --display; run `garfield analyze --suggest` again",
				opts.filter, path)
		}
		printStoredSuggestion(rec)
		return nil
	}

	filters := store.Filters()
	sortFilters(filters)
	if len(filters) == 0 {
		return fmt.Errorf("no stored suggestions in %q; run `garfield analyze --suggest` first", path)
	}

	shown := 0
	for _, filter := range filters {
		rec, _ := store.Lookup(filter)
		if rec.Display == nil {
			fmt.Fprintf(os.Stderr,
				"  warning: suggestion for %s in %q predates --display; run `garfield analyze --suggest` again\n",
				filter, path)
			continue
		}
		printStoredSuggestion(rec)
		shown++
	}
	if shown == 0 {
		return fmt.Errorf("no displayable suggestions in %q: every record predates --display; run `garfield analyze --suggest` again", path)
	}
	return nil
}
