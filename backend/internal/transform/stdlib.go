package transform

import "github.com/expr-lang/expr"

// stdlib is the DESIGN §7.3 standard library beyond the allowed expr builtins. It is extended only
// by platform code releases, never per transformer. Every function is pure.
var stdlib = map[string]func(params ...any) (any, error){}

func stdlibOptions() []expr.Option {
	opts := make([]expr.Option, 0, len(stdlib))
	for name, fn := range stdlib {
		opts = append(opts, expr.Function(name, fn))
	}
	return opts
}
