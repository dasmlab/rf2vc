package redfish

import "context"

type clientKey struct{}

// WithClient records the authenticated Redfish client (Basic Auth username)
// so inbound activity shows which BMH credential made the call.
func WithClient(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, clientKey{}, name)
}

func clientFrom(ctx context.Context) string {
	s, _ := ctx.Value(clientKey{}).(string)
	return s
}
