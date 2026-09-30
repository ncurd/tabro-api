// billing-migrate is an operator CLI, deliberately outside the gateway's HTTP
// surface. Set BILLING_MIGRATION_DSN with an isolated database first.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/billingmigration"
	_ "github.com/lib/pq"
)

func readJSON(path string, out any) error {
	if path == "" {
		return errors.New("--input is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return errors.New("input must contain one JSON object")
	}
	return nil
}
func run() error {
	command := flag.String("command", "", "map, shadow, drain, freeze, export, activate, register-workspace, bind-credential, export-redeem, inspect, replay-outbox")
	userID := flag.Int64("user", 0, "source user ID")
	batch := flag.String("batch", "", "stable migration batch ID")
	input := flag.String("input", "", "reviewed mapping/evidence/target receipt JSON")
	output := flag.String("output", "", "exclusive snapshot output file (0600)")
	timeZone := flag.String("timezone", "", "original subscription timezone, e.g. Asia/Shanghai")
	reference := flag.String("reference", "", "operator change reference")
	apply := flag.Bool("apply", false, "apply reviewed operation; absent means validate input only")
	flag.Parse()
	var mapping billingmigration.Mapping
	var evidence billingmigration.DrainEvidence
	var receipt billingmigration.TargetReceipt
	var workspace billingmigration.WorkspaceRegistration
	var credential billingmigration.CredentialRegistration
	var replay billingmigration.ReplayRequest
	switch *command {
	case "inspect":
		if *output == "" {
			return errors.New("inspect requires a private --output file")
		}
	case "register-workspace":
		if err := readJSON(*input, &workspace); err != nil {
			return err
		}
	case "bind-credential":
		if err := readJSON(*input, &credential); err != nil {
			return err
		}
	case "replay-outbox":
		if err := readJSON(*input, &replay); err != nil {
			return err
		}
	case "map":
		if err := readJSON(*input, &mapping); err != nil {
			return err
		}
	case "freeze":
		if err := readJSON(*input, &evidence); err != nil {
			return err
		}
	case "activate":
		if err := readJSON(*input, &receipt); err != nil {
			return err
		}
	case "shadow":
		if *userID <= 0 || *reference == "" {
			return errors.New("shadow requires --user and --reference")
		}
	case "drain":
		if *batch == "" || *reference == "" {
			return errors.New("drain requires --batch and --reference")
		}
	case "export-redeem":
		if *output == "" || *batch == "" || *reference == "" || *userID <= 0 {
			return errors.New("export-redeem requires --output, --batch transfer ID, --reference and --user source code ID")
		}
	case "export":
		if *output == "" || *timeZone == "" || *batch == "" {
			return errors.New("export requires --output, --timezone and --batch")
		}
	default:
		return errors.New("choose a documented --command; run --help for supported operations")
	}
	if (*command == "drain" || *command == "freeze" || *command == "export" || *command == "activate") && *userID <= 0 {
		return errors.New("positive --user is required")
	}
	if !*apply {
		fmt.Println("Input parsed. No database was opened. Use --apply after reviewing the mapping and migration evidence.")
		return nil
	}
	dsn := os.Getenv("BILLING_MIGRATION_DSN")
	if dsn == "" {
		return errors.New("BILLING_MIGRATION_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	source := billingmigration.Source{DB: db}
	switch *command {
	case "inspect":
		var report []byte
		report, err = source.Inspect(ctx)
		if err == nil {
			err = writePrivateOutput(*output, report)
		}
	case "register-workspace":
		err = source.RegisterWorkspace(ctx, workspace)
	case "bind-credential":
		err = source.RegisterCredential(ctx, credential)
	case "replay-outbox":
		err = source.Replay(ctx, replay)
	case "map":
		err = source.Map(ctx, mapping)
	case "shadow":
		err = source.Shadow(ctx, *userID, *reference)
	case "drain":
		err = source.Drain(ctx, *userID, *batch, *reference)
	case "freeze":
		err = source.Freeze(ctx, *userID, *batch, evidence)
	case "activate":
		err = source.Activate(ctx, *userID, receipt)
	case "export-redeem":
		var encoded []byte
		var hash string
		encoded, hash, err = source.ExportRedeem(ctx, *userID, *batch, *reference)
		if err == nil {
			err = writePrivateOutput(*output, encoded)
		}
		if err == nil {
			fmt.Printf("Frozen redemption exported. SHA-256: %s\n", hash)
		}
	case "export":
		var snapshot billingmigration.Snapshot
		var encoded []byte
		var hash string
		snapshot, encoded, hash, err = source.Export(ctx, *userID, *batch, *timeZone)
		if err != nil {
			break
		}
		// Never truncate an earlier snapshot on a retry. Existing bytes must match.
		if previous, e := os.ReadFile(*output); e == nil {
			if string(previous) != string(encoded) {
				return errors.New("output exists with different snapshot bytes")
			}
		} else if !os.IsNotExist(e) {
			return e
		} else {
			var f *os.File
			f, err = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				break
			}
			_, err = f.Write(encoded)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				break
			}
		}
		err = source.RecordExport(ctx, snapshot, hash)
		if err == nil {
			fmt.Printf("Snapshot recorded. SHA-256: %s\n", hash)
		}
	}
	if err != nil {
		return err
	}
	fmt.Println("Migration operation completed.")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writePrivateOutput(path string, encoded []byte) error {
	if previous, err := os.ReadFile(path); err == nil {
		if string(previous) != string(encoded) {
			return errors.New("output already contains different bytes")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(encoded)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
