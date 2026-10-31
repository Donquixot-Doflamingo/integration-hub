# Integration and contract test scaffolding

## Shared containers in TestMain (testcontainers-go)
The module API changed across versions (`RunContainer` → `Run`); check the version pinned in `go.mod` and its docs before copying.
```go
//go:build integration

package itest

var (
	pgDSN    string
	redisURL string
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()
	if dsn := os.Getenv("HUB_TEST_POSTGRES_DSN"); dsn != "" { // docker compose mode
		pgDSN, redisURL = dsn, os.Getenv("HUB_TEST_REDIS_URL")
	} else {
		pg, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("hub"), postgres.WithUsername("hub"), postgres.WithPassword("hub"),
			postgres.BasicWaitStrategies())
		if err != nil {
			return 0, fmt.Errorf("start postgres: %w", err)
		}
		defer func() { _ = pg.Terminate(ctx) }()
		if pgDSN, err = pg.ConnectionString(ctx, "sslmode=disable"); err != nil {
			return 0, err
		}
		rd, err := tcredis.Run(ctx, "redis:7-alpine")
		if err != nil {
			return 0, fmt.Errorf("start redis: %w", err)
		}
		defer func() { _ = rd.Terminate(ctx) }()
		if redisURL, err = rd.ConnectionString(ctx); err != nil {
			return 0, err
		}
	}
	if err := migrateUp(ctx, pgDSN); err != nil { // goose, same migrations as prod
		return 0, err
	}
	return m.Run(), nil // goleak: call goleak.Find() here if you want leak checks too
}
```
(`run` exists so the `defer`s execute before `os.Exit`.)

Isolation options, cheapest first: unique ids per test → `TRUNCATE` in `t.Cleanup` (non-parallel) → per-test schema (`CREATE SCHEMA t_<rand>` + `search_path` in the DSN) → per-test database from a template (`CREATE DATABASE x TEMPLATE hub_migrated`).

## Testing migrations
- `up` from empty → `down` to zero → `up` again must succeed (catches broken Down).
- Run sqlc `vet`/compile in CI so queries match the migrated schema.

## Contract validation with kin-openapi
Load the spec once per package. kin-openapi's OpenAPI 3.1 support is newer than its 3.0 support: pin a version that handles the 3.1 features `contracts/` uses and keep a test that loads and `Validate`s every file, so an unsupported construct fails loudly rather than skipping validation.
```go
type contract struct {
	doc    *openapi3.T
	router routers.Router
}

func loadContract(t testing.TB, path string) *contract {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(path)
	require.NoError(t, err)
	require.NoError(t, doc.Validate(loader.Context))
	r, err := gorillamux.NewRouter(doc)
	require.NoError(t, err)
	return &contract{doc: doc, router: r}
}

// Do sends req to h and validates both directions against the contract.
func (c *contract) Do(t testing.TB, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	route, params, err := c.router.FindRoute(req)
	require.NoError(t, err, "operation not in contract: %s %s", req.Method, req.URL.Path)

	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
	}
	req.Body = io.NopCloser(bytes.NewReader(reqBody))
	in := &openapi3filter.RequestValidationInput{
		Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	require.NoError(t, openapi3filter.ValidateRequest(req.Context(), in), "request violates contract")
	req.Body = io.NopCloser(bytes.NewReader(reqBody))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in,
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Body:                   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true}, // undeclared status = failure
	}
	require.NoError(t, openapi3filter.ValidateResponse(req.Context(), out), "response violates contract: %s", rec.Body.String())
	return rec
}
```
`FindRoute` matches against the spec's `servers` URLs: build test requests on a host/base path the spec declares (or a relative `servers: [{url: /}]` entry), otherwise every lookup fails with "no matching operation".

Negative tests (invalid body → 400) skip request validation but still validate the response: add a `DoUnvalidatedRequest` variant.

Route coverage test: walk the chi router (`chi.Walk`) and the spec paths; fail on any route without an operation or operation without a route.

## In-process mockvendor
```go
mv := httptest.NewServer(mockvendor.NewHandler(mockvendor.Options{})) // name per the real package
t.Cleanup(mv.Close)
// point the dev vendor definition's base URL at mv.URL in the test snapshot
```
Use the control client to program routes and read the request log. Don't assert on timing with tight bounds; use tolerances.
