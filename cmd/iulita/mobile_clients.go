package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	mobileruntime "github.com/iulita-ai/iulita/internal/mobile/runtime"
)

func runMobileClientCommand(args []string) error {
	if len(args) < 1 {
		return errors.New("mobile-client requires register, list, disable or rotate-secret")
	}
	action := args[0]
	flags := flag.NewFlagSet("mobile-client "+action, flag.ContinueOnError)
	settingsPath := flags.String("settings", "", "absolute runtime settings path")
	registrationPath := flags.String("registration", "", "registration JSON path")
	clientID := flags.String("client-id", "", "registered client ID")
	secretPath := flags.String("secret-out", "", "new absolute private secret output file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *settingsPath == "" {
		return errors.New("--settings is required; positional arguments are not accepted")
	}
	settings, err := mobileruntime.ReadSettings(*settingsPath)
	if err != nil {
		return err
	}
	operation := mobileruntime.ClientOperation{Action: action, ClientID: *clientID, SecretPath: *secretPath}
	if action == "register" {
		if *clientID != "" || *registrationPath == "" {
			return errors.New("register requires --registration and no --client-id")
		}
		info, statErr := os.Lstat(*registrationPath) //nolint:gosec // Explicit offline operator input path.
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 16<<10 {
			return errors.New("invalid registration file")
		}
		body, readErr := os.ReadFile(*registrationPath) //nolint:gosec // Explicit offline operator input path.
		if readErr != nil {
			return readErr
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		var registration oauthstore.ClientRegistration
		if decoder.Decode(&registration) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("invalid client registration JSON")
		}
		operation.Registration = registration
	} else if *registrationPath != "" || (action == "list" && (*clientID != "" || *secretPath != "")) || (action == "disable" && (*clientID == "" || *secretPath != "")) || (action == "rotate-secret" && *clientID == "") {
		return errors.New("invalid client operation flags")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	clients, err := mobileruntime.ManageClients(ctx, settings, operation)
	if err != nil {
		return err
	}
	if action == "list" {
		return json.NewEncoder(os.Stdout).Encode(clients)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"operation": action, "status": "completed"})
}
