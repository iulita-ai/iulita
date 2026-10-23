package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iulita-ai/iulita/internal/mcpworker"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	socket := flag.String("socket", "", "private Unix socket")
	listen := flag.String("listen", ":8444", "TLS listen address")
	cert := flag.String("tls-cert", "", "TLS certificate")
	tlsKey := flag.String("tls-key", "", "TLS key")
	keyFile := flag.String("key-file", "", "private shared HMAC key file")
	profiles := flag.String("profiles", "", "approved rootless Podman image profiles JSON file")
	state := flag.String("state-dir", "", "private persistent nonce directory")
	flag.Parse()
	if err := disableCoreDumps(); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return errors.New("MCP broker requires a non-root user and rootless Podman")
	}
	info, e := os.Stat(*keyFile)
	if e != nil || info.Mode().Perm()&0o077 != 0 {
		return errors.New("MCP key must be private (0o600)")
	}
	key, e := os.ReadFile(*keyFile)
	if e != nil {
		return errors.New("MCP key unavailable")
	}
	data, e := os.ReadFile(*profiles)
	if e != nil {
		return errors.New("MCP profiles unavailable")
	}
	var p map[string]mcpworker.Profile
	if json.Unmarshal(data, &p) != nil {
		return errors.New("invalid MCP profiles")
	}
	check, stopCheck := context.WithTimeout(context.Background(), 10*time.Second)
	infoCmd := exec.CommandContext(check, "podman", "info", "--format", "{{.Host.Security.Rootless}}")
	output, podmanErr := infoCmd.Output()
	stopCheck()
	if podmanErr != nil || strings.TrimSpace(string(output)) != "true" {
		return errors.New("MCP broker requires a working rootless Podman installation")
	}
	handler, e := mcpworker.NewBroker(key, p, *state)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		stop, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if shutdownErr := server.Shutdown(stop); shutdownErr != nil {
			slog.Warn("MCP broker shutdown incomplete")
		}
	}()
	if *socket != "" {
		ln, err := net.Listen("unix", *socket)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := ln.Close(); closeErr != nil {
				slog.Debug("MCP socket cleanup incomplete")
			}
		}()
		defer func() {
			if removeErr := os.Remove(*socket); removeErr != nil {
				slog.Warn("MCP socket cleanup incomplete")
			}
		}()
		if os.Chmod(*socket, 0o600) != nil {
			return errors.New("private socket required")
		}
		e = server.Serve(ln)
	} else {
		if *cert == "" || *tlsKey == "" {
			return errors.New("TLS or a private Unix socket required")
		}
		e = server.ListenAndServeTLS(*cert, *tlsKey)
	}
	cancel()
	cleanup, stopCleanup := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopCleanup()
	if err := handler.Shutdown(cleanup); err != nil {
		return fmt.Errorf("MCP worker cleanup incomplete: %w", err)
	}
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
