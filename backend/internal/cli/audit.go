package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
)

func runAudit(ctx context.Context, log *slog.Logger, args []string) error {
	cfg := defaultConfig()
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	pg, live, err := connect(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pg.Close()
	defer func() { _ = live.Close() }()

	a, err := pg.Audit(ctx)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(w, "deposited\t%s\t\n", money(a.Deposited))
	fmt.Fprintf(w, "spendable\t%s\t\n", money(a.Spendable))
	fmt.Fprintf(w, "escrowed in open markets\t%s\t\n", money(a.Escrowed))
	fmt.Fprintf(w, "house rake\t%s\t\n", money(a.Rake))
	fmt.Fprintf(w, "\t\t\n")
	fmt.Fprintf(w, "spendable + escrow + rake\t%s\t\n", money(a.Spendable+a.Escrowed+a.Rake))
	if err := w.Flush(); err != nil {
		return err
	}

	if diff := a.Deposited - (a.Spendable + a.Escrowed + a.Rake); diff != 0 {
		return fmt.Errorf("money is not conserved: %s unaccounted for", money(diff))
	}
	fmt.Println("\nconserved: every cent deposited is spendable, escrowed, or rake")
	return nil
}
