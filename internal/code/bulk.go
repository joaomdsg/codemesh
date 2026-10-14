package code

import "context"

type bulkKey struct{}

// Bulk marks ctx as one load of a batch, so a frontend may keep a process
// running between them.
func Bulk(ctx context.Context) context.Context { return context.WithValue(ctx, bulkKey{}, true) }

// InBulk reports whether ctx came from Bulk.
func InBulk(ctx context.Context) bool { b, _ := ctx.Value(bulkKey{}).(bool); return b }
