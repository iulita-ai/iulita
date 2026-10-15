package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	mobileruntime "github.com/iulita-ai/iulita/internal/mobile/runtime"
)

// These are explicit local operator commands. Normal server startup never
// creates a realm, resets an epoch or repairs missing keys automatically.
func runMobileCommand(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	settingsPath := flags.String("settings", "", "absolute path to mobile runtime JSON settings")
	restore := flags.Bool("restore", false, "invalidate restored mobile state with a fresh signing epoch")
	recoverCredentials := flags.Bool("recover", false, "recover an interrupted offline rotation; never run concurrently with the service")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *settingsPath == "" || flags.NArg() != 0 {
		return errors.New("mobile command requires --settings <file>")
	}
	settings, err := mobileruntime.ReadSettings(*settingsPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if command == "mobile-init" {
		if *restore || *recoverCredentials {
			return errors.New("restore/recover belong to mobile-rotate")
		}
		err = mobileruntime.Initialize(ctx, settings)
	} else {
		err = oauthstore.RotateOffline(ctx, settings.RealmDir, settings.OAuthDB, settings.AccountDB, settings.Issuer, settings.Audience, *restore, *recoverCredentials)
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", command, err)
	}
	_, err = fmt.Fprintln(os.Stdout, "Mobile offline operation completed. No listener or deployment was started.")
	return err
}
