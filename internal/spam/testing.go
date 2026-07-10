package spam

import "context"

// SetLookupsForTest injects fake DNS lookup functions. Test helper only —
// production code must not call this.
func SetLookupsForTest(
	c *Checker,
	lookupHost func(ctx context.Context, host string) ([]string, error),
	lookupAddr func(ctx context.Context, addr string) ([]string, error),
) {
	c.lookupHost = lookupHost
	c.lookupAddr = lookupAddr
}
