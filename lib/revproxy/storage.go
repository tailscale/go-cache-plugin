package revproxy

import (
	"context"
	"expvar"

	"github.com/creachadair/gocache"
)

type Storage interface {
	Close(context.Context) error
	Get(context.Context, string) (string, string, error)
	Put(context.Context, gocache.Object) (string, error)
	SetMetrics(context.Context, *expvar.Map)
}
