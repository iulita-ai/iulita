// ssh-worker runs only native SSH transport. It must run as a separate,
// restricted user/container without the bot's filesystem or credentials.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iulita-ai/iulita/internal/remoteaccess"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	socket := flag.String("socket", "", "private unix socket path (use instead of TLS)")
	listen := flag.String("listen", ":8443", "TLS listen address")
	cert := flag.String("tls-cert", "", "TLS certificate path")
	tlsKey := flag.String("tls-key", "", "TLS private key path")
	keyFile := flag.String("key-file", "", "shared protocol key file, at least 32 random bytes")
	state := flag.String("state-dir", "", "private persistent nonce directory")
	networks := flag.String("allow-cidrs", "", "comma-separated deployment-owned SSH destination networks")
	flag.Parse()
	if err := disableCoreDumps(); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return errors.New("SSH worker must run as a non-root user")
	}
	info, err := os.Stat(*keyFile)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return errors.New("worker key file must be private (0o600)")
	}
	key, err := os.ReadFile(*keyFile)
	if err != nil {
		return errors.New("cannot read worker key")
	}
	handler, err := remoteaccess.NewWorkerHandler(key, strings.Split(*networks, ","), *state)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 7 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(shutdown); shutdownErr != nil {
			fmt.Fprintln(os.Stderr, "worker shutdown incomplete")
		}
	}()
	if *socket != "" {
		// Never unlink an existing path: a second worker must not steal a live socket.
		ln, listenErr := net.Listen("unix", *socket)
		if listenErr != nil {
			return listenErr
		}
		defer func() {
			if closeErr := ln.Close(); closeErr != nil {
				return
			}
		}()
		defer func() {
			if removeErr := os.Remove(*socket); removeErr != nil && !os.IsNotExist(removeErr) {
				fmt.Fprintln(os.Stderr, "worker socket cleanup incomplete")
			}
		}()
		if chmodErr := os.Chmod(*socket, 0o600); chmodErr != nil {
			return chmodErr
		}
		err = server.Serve(ln)
	} else {
		if *cert == "" || *tlsKey == "" {
			return errors.New("TLS certificate/key required unless using a private unix socket")
		}
		server.TLSConfig, err = workerTLSConfig(*cert, *tlsKey)
		if err != nil {
			return err
		}
		err = server.ListenAndServeTLS("", "")
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
