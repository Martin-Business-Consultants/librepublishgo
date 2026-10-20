package cms

import "context"

// change is what a write tells the rest of the install. Webhooks and
// plugins hear of Event, with Data (the webhook payload), and the deploy
// rebuilds what Tags cover; a change with no Event only touches the site.
type change struct {
	Event string
	Data  any
	Tags  []string
}

// listener hears every change.
type listener func(ctx context.Context, ch change)

// publish tells every listener, in the order they were added.
func (a *App) publish(ctx context.Context, ch change) {
	for _, l := range a.listeners {
		l(ctx, ch)
	}
}

// listen wires who hears what: webhooks, the deploy and plugins hear of
// changes, and any change may have moved the schema's digest.
func (a *App) listen() {
	a.listeners = []listener{
		func(ctx context.Context, ch change) {
			if ch.Event != "" {
				a.hooks.Deliver(ctx, ch.Event, ch.Data)
			}
		},
		func(ctx context.Context, ch change) {
			if len(ch.Tags) > 0 {
				a.deploys.Changed(ctx, ch.Tags...)
			}
		},
		func(ctx context.Context, ch change) {
			if ch.Event != "" {
				a.plugins.Emit(ctx, ch.Event, ch.Data)
			}
		},
		func(_ context.Context, ch change) {
			if ch.Event != schemaChangedEvent {
				a.schemaTouched()
			}
		},
	}
}
