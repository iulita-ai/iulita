package telegrambusiness

// RuntimeSource reports only the currently published exact instance/bot delivery
// generation. An empty value denies admission; it performs no network request.
type RuntimeSource interface {
	BusinessGeneration(Scope) string
}
