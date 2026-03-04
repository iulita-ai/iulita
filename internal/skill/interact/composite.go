package interact

// CompositeFactory delegates to multiple channel factories and returns the
// first non-nil PromptAsker. Used in multi-channel deployments.
type CompositeFactory struct {
	factories []PromptAskerFactory
}

// NewCompositeFactory creates a CompositeFactory from multiple factories.
func NewCompositeFactory(factories ...PromptAskerFactory) *CompositeFactory {
	return &CompositeFactory{factories: factories}
}

// PrompterFor returns the first non-nil PromptAsker from the registered factories.
func (c *CompositeFactory) PrompterFor(chatID string) PromptAsker {
	for _, f := range c.factories {
		if p := f.PrompterFor(chatID); p != nil {
			return p
		}
	}
	return nil
}
