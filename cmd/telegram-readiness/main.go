// telegram-readiness performs GET-equivalent read-only Bot API identity checks.
// It never polls updates, sends messages, edits account settings or prints tokens.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/go-telegram/bot"
	"github.com/iulita-ai/iulita/internal/channel/telegram"
)

func main() {
	env := flag.String("token-env", "IULITA_TELEGRAM_TOKEN", "Environment variable containing this bot's token")
	proxy := flag.String("proxy", "", "Optional HTTP proxy URL")
	test := flag.Bool("test-environment", false, "Use Telegram test server; requires a separate test bot token")
	flag.Parse()
	token := os.Getenv(*env)
	if token == "" {
		fmt.Fprintln(os.Stderr, "Bot token environment variable is empty")
		os.Exit(1)
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		fmt.Fprintln(os.Stderr, "Default HTTP transport is unavailable")
		os.Exit(1)
	}
	transport := defaultTransport.Clone()
	if *proxy != "" {
		u, e := url.Parse(*proxy)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			fmt.Fprintln(os.Stderr, "Invalid proxy URL")
			os.Exit(1)
		}
		transport.Proxy = http.ProxyURL(u)
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	opts := []bot.Option{bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, client)}
	if *test {
		opts = append(opts, bot.UseTestEnvironment())
	}
	b, e := bot.New(token, opts...)
	if e != nil {
		fmt.Fprintln(os.Stderr, "Could not initialize Bot API client")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	result, e := telegram.InspectBotIdentity(ctx, b)
	cancel()
	if e != nil {
		fmt.Fprintln(os.Stderr, "Telegram identity check failed; verify token, proxy and environment")
		os.Exit(1)
	}
	if e = json.NewEncoder(os.Stdout).Encode(result); e != nil {
		fmt.Fprintln(os.Stderr, "Could not write identity report")
		os.Exit(1)
	}
}
