package interact

import "context"

// TurnAskerFactory never consults a mutable per-chat last-sender slot. Actor
// comes from a single authenticated incoming turn and stays immutable.
type TurnAskerFactory interface {
	PrompterForActor(context.Context, string, Actor) PromptAsker
	ConfirmerForActor(context.Context, string, Actor) ConfirmationAsker
}
type turnActorKey struct{}

// WithTurnActor captures one authenticated interaction actor in the request context.
func WithTurnActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, turnActorKey{}, actor)
}

// TurnActorFrom returns the authenticated actor captured by the current turn.
func TurnActorFrom(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(turnActorKey{}).(Actor)
	return actor, ok && actor.UserID != "" && actor.ExternalID != ""
}
