package config

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	ollamallm "github.com/iulita-ai/iulita/internal/llm/ollama"
	openaillm "github.com/iulita-ai/iulita/internal/llm/openai"
)

// SetupResult contains the results of the interactive setup wizard.
type SetupResult struct {
	// Values is a map of config key → value collected from the wizard.
	Values  map[string]string
	SavedTo string // "keyring" or "config"
}

// RunSetupWizard runs an interactive setup wizard driven by CoreConfigSchema().
// It prompts for each wizard-visible section and saves results to keyring + config file.
func RunSetupWizard(paths *Paths) (*SetupResult, error) {
	reader := bufio.NewReader(os.Stdin)
	result := &SetupResult{Values: make(map[string]string)}
	ks := NewKeyStore(paths)

	fmt.Println()
	fmt.Println("Welcome to Iulita! Let's set up your assistant.")
	fmt.Println()

	// Step 1: Choose LLM provider(s).
	llmProviders := []struct {
		name    string
		section string
		label   string
	}{
		{"claude", "claude", "Claude (Anthropic)"},
		{"openai", "openai", "OpenAI-Compatible"},
		{"ollama", "ollama", "Ollama (Local)"},
	}

	fmt.Println("Which LLM provider(s) would you like to use?")
	for i, p := range llmProviders {
		marker := "  "
		if i == 0 {
			marker = "* "
		}
		fmt.Printf("  %s%d) %s\n", marker, i+1, p.label)
	}
	fmt.Println()
	answer, err := promptLine(reader, "Enter numbers separated by commas [1]: ")
	if err != nil {
		return nil, err
	}

	selectedProviders := map[string]bool{}
	if answer == "" {
		selectedProviders["claude"] = true
	} else {
		for _, part := range strings.Split(answer, ",") {
			part = strings.TrimSpace(part)
			if n, err := strconv.Atoi(part); err == nil && n >= 1 && n <= len(llmProviders) {
				selectedProviders[llmProviders[n-1].name] = true
			}
		}
	}
	if len(selectedProviders) == 0 {
		selectedProviders["claude"] = true
	}

	sections := WizardSections()
	sort.Slice(sections, func(i, j int) bool {
		return sections[i].WizardOrder < sections[j].WizardOrder
	})

	for _, section := range sections {
		// LLM provider sections: skip if not selected.
		isLLMSection := section.Name == "claude" || section.Name == "openai" || section.Name == "ollama"
		if isLLMSection && !selectedProviders[section.Name] {
			continue
		}

		// Sort fields by WizardOrder.
		fields := make([]ConfigField, len(section.Fields))
		copy(fields, section.Fields)
		sort.Slice(fields, func(i, j int) bool {
			return fields[i].WizardOrder < fields[j].WizardOrder
		})

		// Non-LLM optional sections get a gate question.
		if section.Optional && !isLLMSection {
			hasRequired := false
			for _, f := range fields {
				if f.Required {
					hasRequired = true
					break
				}
			}
			if !hasRequired {
				fmt.Printf("\n── %s (%s) ──\n", section.Label, section.Description)
				answer, err := promptLine(reader, fmt.Sprintf("Configure %s? [y/N]: ", section.Label))
				if err != nil {
					return nil, err
				}
				if !isYes(answer) {
					continue
				}
			}
		} else {
			fmt.Printf("\n── %s ──\n", section.Label)
		}

		for _, field := range fields {
			// For dynamic model fields, try to fetch available models.
			if field.ModelSource != "" {
				dynamicOpts := fetchModelsForWizard(field.ModelSource, result.Values)
				if len(dynamicOpts) > 0 {
					field.Type = FieldSelect
					field.Options = dynamicOpts
				}
			}

			value, err := promptField(reader, field)
			if err != nil {
				return nil, err
			}
			if value != field.Default {
				// Save when user chose a non-default value (including empty = explicitly disabled).
				result.Values[field.Key] = value
			}
		}
	}

	// Validate required fields — only for selected providers.
	for _, section := range WizardSections() {
		isLLMSection := section.Name == "claude" || section.Name == "openai" || section.Name == "ollama"
		if isLLMSection && !selectedProviders[section.Name] {
			continue
		}
		for _, f := range section.Fields {
			if f.Required {
				if v, ok := result.Values[f.Key]; !ok || v == "" {
					return nil, fmt.Errorf("%s is required", f.Label)
				}
			}
		}
	}

	// Store selected provider for config (routing.default_provider).
	// Pick the first selected provider as default.
	for _, p := range llmProviders {
		if selectedProviders[p.name] {
			result.Values["routing.default_provider"] = p.name
			break
		}
	}

	// Save secrets to keyring, non-secrets to config file.
	secrets := SchemaSecretKeys()
	secretValues := make(map[string]string)
	nonSecretValues := make(map[string]string)

	for k, v := range result.Values {
		if secrets[k] {
			secretValues[k] = v
		} else {
			nonSecretValues[k] = v
		}
	}

	// Try keyring for secrets.
	keyringUsed := false
	if ks.KeyringAvailable() && len(secretValues) > 0 {
		allSaved := true
		for key, value := range secretValues {
			account := keyringAccountForKey(key)
			if account == "" {
				// No keyring mapping — will go to config file.
				nonSecretValues[key] = value
				continue
			}
			if err := ks.SaveSecret(account, value); err != nil {
				allSaved = false
				nonSecretValues[key] = value
			}
		}
		if allSaved {
			keyringUsed = true
			fmt.Println("\nSecrets saved to system keyring.")
		}
	} else {
		// No keyring — all secrets go to config file.
		for k, v := range secretValues {
			nonSecretValues[k] = v
		}
	}

	// Always write config file after init — even with defaults, so the user has a file to edit.
	if err := paths.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("creating config directory: %w", err)
	}
	configFile := paths.ConfigFile()
	if len(nonSecretValues) > 0 {
		if err := writeConfigFromValues(configFile, nonSecretValues); err != nil {
			return nil, fmt.Errorf("writing config: %w", err)
		}
	} else {
		// No non-secret overrides — write a minimal config with a comment.
		content := "# Iulita configuration\n# Edit this file or use the dashboard to configure.\n# Secrets are stored in the system keyring.\n"
		if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
			return nil, fmt.Errorf("writing config: %w", err)
		}
	}
	_ = os.Chmod(configFile, 0600)
	fmt.Printf("Config saved to %s\n", configFile)

	if keyringUsed {
		result.SavedTo = "keyring"
	} else {
		result.SavedTo = "config"
	}

	return result, nil
}

// keyringAccountForKey returns the keyring account name for a secret key.
func keyringAccountForKey(key string) string {
	switch key {
	case "claude.api_key":
		return keyringAccountAPI
	case "telegram.token":
		return keyringAccountTG
	case "openai.api_key":
		return "openai-api-key"
	default:
		return ""
	}
}

// promptField prompts the user for a single config field value.
func promptField(reader *bufio.Reader, field ConfigField) (string, error) {
	switch field.Type {
	case FieldSelect:
		return promptSelect(reader, field)
	case FieldBool:
		return promptBool(reader, field)
	case FieldSecret:
		return promptSecret(reader, field)
	default:
		return promptString(reader, field)
	}
}

// promptString prompts for a free-text value.
func promptString(reader *bufio.Reader, field ConfigField) (string, error) {
	prompt := field.Label
	if field.Default != "" {
		prompt += fmt.Sprintf(" [%s]", field.Default)
	}
	if !field.Required {
		prompt += " (Enter to skip)"
	}
	prompt += ": "

	val, err := promptLine(reader, prompt)
	if err != nil {
		return "", err
	}
	if val == "" {
		return field.Default, nil
	}
	return val, nil
}

// promptSecret prompts for a secret value (shown as typed, but labeled as secret).
func promptSecret(reader *bufio.Reader, field ConfigField) (string, error) {
	suffix := ""
	if field.Required {
		suffix = " (required)"
	} else {
		suffix = " (Enter to skip)"
	}
	val, err := promptLine(reader, fmt.Sprintf("%s%s: ", field.Label, suffix))
	if err != nil {
		return "", err
	}
	return val, nil
}

// promptSelect shows numbered options for selection.
func promptSelect(reader *bufio.Reader, field ConfigField) (string, error) {
	options := field.Options
	if len(options) == 0 {
		return promptString(reader, field)
	}

	// Find default index.
	defaultIdx := -1
	for i, opt := range options {
		if opt == field.Default {
			defaultIdx = i
			break
		}
	}

	fmt.Printf("%s:\n", field.Label)
	for i, opt := range options {
		label := opt
		if label == "" {
			label = "(disabled)"
		}
		marker := "  "
		if i == defaultIdx {
			marker = "* "
			label += " (default)"
		}
		fmt.Printf("  %s%d) %s\n", marker, i+1, label)
	}

	prompt := "Choice"
	if defaultIdx >= 0 {
		prompt += fmt.Sprintf(" [%d]", defaultIdx+1)
	}
	prompt += ": "

	val, err := promptLine(reader, prompt)
	if err != nil {
		return "", err
	}
	if val == "" {
		return field.Default, nil
	}

	// Accept number or raw value.
	if n, err := strconv.Atoi(val); err == nil && n >= 1 && n <= len(options) {
		return options[n-1], nil
	}
	// Accept raw option name.
	for _, opt := range options {
		if strings.EqualFold(val, opt) {
			return opt, nil
		}
	}
	// Accept "custom" for free-text entry.
	if strings.EqualFold(val, "custom") {
		custom, err := promptLine(reader, "Enter custom value: ")
		if err != nil {
			return "", err
		}
		return custom, nil
	}
	return val, nil
}

// promptBool shows a y/N prompt.
func promptBool(reader *bufio.Reader, field ConfigField) (string, error) {
	defaultBool := field.Default == "true"
	var prompt string
	if defaultBool {
		prompt = fmt.Sprintf("%s [Y/n]: ", field.Label)
	} else {
		prompt = fmt.Sprintf("%s [y/N]: ", field.Label)
	}
	val, err := promptLine(reader, prompt)
	if err != nil {
		return "", err
	}
	if val == "" {
		return field.Default, nil
	}
	if isYes(val) {
		return "true", nil
	}
	return "false", nil
}

// promptLine reads a trimmed line from the reader.
func promptLine(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// fetchModelsForWizard attempts to fetch available models from a provider.
// Uses values already collected in the wizard (API key, URL) to make the request.
func fetchModelsForWizard(source ModelSource, values map[string]string) []string {
	httpClient := &http.Client{}

	switch source {
	case ModelSourceOpenAI:
		apiKey := values["openai.api_key"]
		if apiKey == "" {
			return nil
		}
		baseURL := values["openai.base_url"]
		fmt.Print("  Fetching available models... ")
		models, err := openaillm.ListModels(baseURL, apiKey, httpClient)
		if err != nil {
			fmt.Printf("failed (%v)\n", err)
			return nil
		}
		fmt.Printf("found %d models\n", len(models))
		// Filter to reasonable chat models if there are too many.
		if len(models) > 20 {
			filtered := filterChatModels(models)
			if len(filtered) > 0 {
				return filtered
			}
		}
		return models

	case ModelSourceOllama:
		ollamaURL := values["ollama.url"]
		if ollamaURL == "" {
			ollamaURL = "http://localhost:11434"
		}
		fmt.Print("  Fetching available models... ")
		models, err := ollamallm.ListModels(ollamaURL, httpClient)
		if err != nil {
			fmt.Printf("failed (%v)\n", err)
			return nil
		}
		if len(models) == 0 {
			fmt.Println("no models found (run 'ollama pull <model>' first)")
			return nil
		}
		fmt.Printf("found %d models\n", len(models))
		return models
	}

	return nil
}

// filterChatModels filters OpenAI model list to common chat models.
func filterChatModels(models []string) []string {
	prefixes := []string{"gpt-4", "gpt-3.5", "o1", "o3", "chatgpt"}
	var filtered []string
	for _, m := range models {
		for _, p := range prefixes {
			if strings.HasPrefix(m, p) {
				filtered = append(filtered, m)
				break
			}
		}
	}
	return filtered
}

func isYes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

// writeConfigFromValues writes a config.toml from a map of key=value pairs.
// Groups keys by their TOML section (first dotted segment).
func writeConfigFromValues(path string, values map[string]string) error {
	// Group by section.
	sections := make(map[string][]struct{ key, value string })
	var sectionOrder []string
	for k, v := range values {
		parts := strings.SplitN(k, ".", 2)
		section := parts[0]
		field := k
		if len(parts) == 2 {
			field = parts[1]
		}
		if _, exists := sections[section]; !exists {
			sectionOrder = append(sectionOrder, section)
		}
		sections[section] = append(sections[section], struct{ key, value string }{field, v})
	}
	sort.Strings(sectionOrder)

	var b strings.Builder
	b.WriteString("# Iulita configuration (auto-generated)\n")
	b.WriteString("# Only non-default settings. Edit or delete keys to revert to defaults.\n\n")

	for _, section := range sectionOrder {
		fields := sections[section]
		sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })

		b.WriteString(fmt.Sprintf("[%s]\n", section))
		for _, f := range fields {
			// Determine if value should be quoted or not.
			if isBoolValue(f.value) || isIntValue(f.value) || isFloatValue(f.value) {
				b.WriteString(fmt.Sprintf("%s = %s\n", f.key, f.value))
			} else {
				b.WriteString(fmt.Sprintf("%s = %q\n", f.key, f.value))
			}
		}
		b.WriteString("\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0600)
}

func isBoolValue(s string) bool {
	return s == "true" || s == "false"
}

func isIntValue(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func isFloatValue(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil && strings.Contains(s, ".")
}

// GenerateDefaultConfig writes a fully-commented config.toml with all defaults.
func GenerateDefaultConfig(paths *Paths) (string, error) {
	cfg := DefaultConfig(paths)
	var b strings.Builder

	b.WriteString("# Iulita configuration\n")
	b.WriteString("# Generated with default values. Uncomment and modify as needed.\n")
	b.WriteString("# Secrets can also be set via environment variables (IULITA_ prefix)\n")
	b.WriteString("# or stored in system keyring via 'iulita init'.\n\n")

	b.WriteString("[app]\n")
	b.WriteString(fmt.Sprintf("system_prompt = %q\n", cfg.App.SystemPrompt))
	b.WriteString(fmt.Sprintf("# default_timezone = %q\n", ""))
	b.WriteString(fmt.Sprintf("auto_link_summary = %v\n", cfg.App.AutoLinkSummary))
	b.WriteString(fmt.Sprintf("max_links = %d\n", cfg.App.MaxLinks))
	b.WriteString("\n")

	b.WriteString("[log]\n")
	b.WriteString(fmt.Sprintf("level = %q\n", cfg.Log.Level))
	b.WriteString(fmt.Sprintf("encoding = %q\n", cfg.Log.Encoding))
	b.WriteString("\n")

	b.WriteString("[claude]\n")
	b.WriteString("# Set via IULITA_CLAUDE_API_KEY env var or system keyring\n")
	b.WriteString("# api_key = \"\"\n")
	b.WriteString(fmt.Sprintf("model = %q\n", cfg.Claude.Model))
	b.WriteString(fmt.Sprintf("max_tokens = %d\n", cfg.Claude.MaxTokens))
	b.WriteString(fmt.Sprintf("context_window = %d\n", cfg.Claude.ContextWindow))
	b.WriteString("# base_url = \"\"  # custom API endpoint\n")
	b.WriteString(fmt.Sprintf("request_timeout = %q\n", cfg.Claude.RequestTimeout))
	b.WriteString("# thinking = \"\"  # off/low/medium/high\n")
	b.WriteString("# streaming = false\n")
	b.WriteString("\n")

	b.WriteString("[openai]\n")
	b.WriteString("# api_key = \"\"  # Set via IULITA_OPENAI_API_KEY env var\n")
	b.WriteString("# model = \"\"  # e.g. gpt-4o\n")
	b.WriteString(fmt.Sprintf("max_tokens = %d\n", cfg.OpenAI.MaxTokens))
	b.WriteString("# base_url = \"\"  # custom endpoint for compatible APIs\n")
	b.WriteString("# fallback = false  # use as fallback when Claude fails\n")
	b.WriteString("\n")

	b.WriteString("[ollama]\n")
	b.WriteString("# url = \"http://localhost:11434\"\n")
	b.WriteString("# model = \"\"  # e.g. llama3, mistral\n")
	b.WriteString("\n")

	b.WriteString("[proxy]\n")
	b.WriteString("# url = \"\"  # HTTP/HTTPS/SOCKS5 proxy\n")
	b.WriteString("\n")

	b.WriteString("[telegram]\n")
	b.WriteString("# Set via IULITA_TELEGRAM_TOKEN env var or system keyring\n")
	b.WriteString("# token = \"\"\n")
	b.WriteString("# allowed_ids = []\n")
	b.WriteString(fmt.Sprintf("debounce_window = %q\n", cfg.Telegram.DebounceWindow))
	b.WriteString("\n")

	b.WriteString("[storage]\n")
	b.WriteString(fmt.Sprintf("path = %q\n", cfg.Storage.Path))
	b.WriteString("\n")

	b.WriteString("[server]\n")
	b.WriteString(fmt.Sprintf("enabled = %v\n", cfg.Server.Enabled))
	b.WriteString(fmt.Sprintf("address = %q\n", cfg.Server.Address))
	b.WriteString("\n")

	b.WriteString("[auth]\n")
	b.WriteString("# jwt_secret auto-generated if empty\n")
	b.WriteString(fmt.Sprintf("token_expiry = %q\n", cfg.Auth.TokenExpiry))
	b.WriteString(fmt.Sprintf("refresh_expiry = %q\n", cfg.Auth.RefreshExpiry))
	b.WriteString("\n")

	b.WriteString("[embedding]\n")
	b.WriteString("# provider = \"onnx\"  # enable for hybrid search\n")
	b.WriteString(fmt.Sprintf("model_dir = %q\n", cfg.Embedding.ModelDir))
	b.WriteString("\n")

	b.WriteString("[routing]\n")
	b.WriteString(fmt.Sprintf("enabled = %v\n", cfg.Routing.Enabled))
	b.WriteString(fmt.Sprintf("default_provider = %q\n", cfg.Routing.DefaultProvider))
	b.WriteString("# classification_enabled = false\n")
	b.WriteString("# classification_provider = \"ollama\"\n")
	b.WriteString("# max_actions_per_hour = 0\n")
	b.WriteString("\n")

	b.WriteString("[cache]\n")
	b.WriteString(fmt.Sprintf("response_enabled = %v\n", cfg.Cache.ResponseEnabled))
	b.WriteString(fmt.Sprintf("response_ttl = %q\n", cfg.Cache.ResponseTTL))
	b.WriteString(fmt.Sprintf("response_max_items = %d\n", cfg.Cache.ResponseMaxItems))
	b.WriteString(fmt.Sprintf("embedding_enabled = %v\n", cfg.Cache.EmbeddingEnabled))
	b.WriteString(fmt.Sprintf("embedding_max_items = %d\n", cfg.Cache.EmbeddingMaxItems))
	b.WriteString("\n")

	b.WriteString("[cost]\n")
	b.WriteString(fmt.Sprintf("enabled = %v\n", cfg.Cost.Enabled))
	b.WriteString(fmt.Sprintf("# daily_limit_usd = %.2f\n", cfg.Cost.DailyLimitUSD))
	b.WriteString(fmt.Sprintf("alert_threshold = %.1f\n", cfg.Cost.AlertThreshold))
	b.WriteString("\n")

	b.WriteString("[scheduler]\n")
	b.WriteString(fmt.Sprintf("enabled = %v\n", cfg.Scheduler.Enabled))
	b.WriteString(fmt.Sprintf("poll_interval = %q\n", cfg.Scheduler.PollInterval))
	b.WriteString(fmt.Sprintf("concurrency = %d\n", cfg.Scheduler.Concurrency))
	b.WriteString("\n")

	b.WriteString("# For full configuration reference, see config.toml.example\n")

	return b.String(), nil
}
