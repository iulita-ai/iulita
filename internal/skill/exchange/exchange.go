package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

const apiBaseURL = "https://api.exchangerate-api.com/v4/latest/"

// Skill provides currency exchange rate lookups.
type Skill struct {
	httpClient *http.Client
}

// New creates a new exchange rate skill.
func New(httpClient *http.Client) *Skill {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Skill{httpClient: httpClient}
}

func (s *Skill) Name() string { return "exchange_rate" }

func (s *Skill) Description() string {
	return "Look up current currency exchange rates. " +
		"Converts between any pair of currencies using live rates. " +
		"Supports 160+ currencies (ISO 4217 codes like USD, EUR, GBP, JPY, etc.)."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"from": {
			"type": "string",
			"description": "Base currency code (e.g. USD, EUR, GBP)"
		},
		"to": {
			"type": "string",
			"description": "Target currency code(s), comma-separated for multiple (e.g. \"EUR\" or \"EUR,GBP,JPY\")"
		},
		"amount": {
			"type": "number",
			"description": "Amount to convert (default 1)"
		}
	},
	"required": ["from", "to"]
}`)
}

type exchangeInput struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Amount float64 `json:"amount"`
}

type apiResponse struct {
	Base  string             `json:"base"`
	Date  string             `json:"date"`
	Rates map[string]float64 `json:"rates"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in exchangeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	in.From = strings.TrimSpace(strings.ToUpper(in.From))
	if in.From == "" {
		return "Please specify the source currency code (e.g. USD, EUR).", nil
	}

	if strings.TrimSpace(in.To) == "" {
		return "Please specify the target currency code(s) (e.g. EUR or EUR,GBP).", nil
	}

	if in.Amount == 0 {
		in.Amount = 1
	}

	targets := parseTargets(in.To)
	if len(targets) == 0 {
		return "Please specify at least one target currency code.", nil
	}

	rates, err := s.fetchRates(ctx, in.From)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Exchange rates as of %s (base: %s)\n\n", rates.Date, rates.Base)

	for _, to := range targets {
		rate, ok := rates.Rates[to]
		if !ok {
			fmt.Fprintf(&b, "  %s: currency code not found\n", to)
			continue
		}
		result := in.Amount * rate
		fmt.Fprintf(&b, "  %s %s = %s %s (rate: %s)\n",
			formatAmount(in.Amount), in.From,
			formatAmount(result), to,
			formatRate(rate),
		)
	}

	return b.String(), nil
}

func (s *Skill) fetchRates(ctx context.Context, base string) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+base, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching exchange rates: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exchange rate API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing exchange rate response: %w", err)
	}

	return &result, nil
}

func parseTargets(s string) []string {
	parts := strings.Split(s, ",")
	var targets []string
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToUpper(p))
		if p != "" {
			targets = append(targets, p)
		}
	}
	return targets
}

func formatAmount(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e12 {
		return fmt.Sprintf("%.0f", v)
	}
	if math.Abs(v) < 0.01 {
		return fmt.Sprintf("%.6f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func formatRate(v float64) string {
	if v < 0.01 {
		return fmt.Sprintf("%.6f", v)
	}
	if v < 1 {
		return fmt.Sprintf("%.4f", v)
	}
	return fmt.Sprintf("%.4f", v)
}
