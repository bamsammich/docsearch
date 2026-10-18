package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver every library is read through
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/httpx"
	"github.com/bamsammich/docsearch/internal/library"
	"github.com/bamsammich/docsearch/internal/mcpserver"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/source/site"
	"github.com/bamsammich/docsearch/internal/upload"
)

// routeGroup is the fx value group every mounted service joins. Written
// once because the tag is matched as a string: a typo in one of the three
// places it appears would build an empty group rather than fail.
const routeGroup = `group:"routes"`

// shutdownGrace bounds every OnStop hook together: the HTTP server drains,
// then the PDF engine stops and both database handles close.
const shutdownGrace = 25 * time.Second

// serve builds the dependency tree and runs it until a signal arrives.
//
// fx owns construction order and the reverse-order teardown that goes with
// it. Each provider registers its own OnStop hook next to the thing it
// opened, which is the reason to use fx here: a defer in one long function
// runs only once that function has reached it, so a failure partway through
// startup used to leak whatever had already opened. A hook registered on the
// lifecycle runs whether startup finished or fell over.
func serve(cfg config.Config, log *slog.Logger) error {
	app := fx.New(
		fx.Supply(cfg, log),
		fx.WithLogger(fxLogger),
		fx.StopTimeout(shutdownGrace),
		fx.Provide(
			newPool,
			newExtractor,
			newFormats,
			newLibraries,
			newAccess,
			newUploads,
			grouped(newMCPRoute),
			grouped(newIngestRoute),
			grouped(newDocumentRoute),
			grouped(newJobRoute),
			grouped(newUploadRoute),
			fx.Annotate(newMux, fx.ParamTags(routeGroup)),
			fx.Annotate(newServer, fx.ParamTags("", "", routeGroup)),
		),
		// Nothing depends on the server, so name it as the root the graph is
		// built for. Without this the whole tree is unreachable and fx builds
		// none of it.
		fx.Invoke(func(*http.Server) {}),
	)

	if err := app.Start(context.Background()); err != nil {
		return err
	}
	sig := <-app.Wait()
	if sig.Signal != nil {
		log.Info("signal received", "signal", sig.Signal.String())
	}
	if err := app.Stop(context.Background()); err != nil {
		return err
	}
	// A non-zero code here means the listener stopped on its own and asked
	// the app to shut down. Why it stopped was logged where it happened.
	if sig.ExitCode != 0 {
		return errStoppedServing
	}
	log.Info("shut down cleanly")
	return nil
}

// errStoppedServing reports an exit that no signal asked for.
var errStoppedServing = errors.New("the server stopped serving before a signal arrived")

// fxLogger sends fx's own wiring commentary to the server log at debug
// level, where it stays out of the way until someone turns it up. Failures
// are logged at error level by fx regardless of the level set here.
//
//nolint:ireturn // fx.WithLogger takes a constructor for fxevent.Logger; the port is the return type.
func fxLogger(log *slog.Logger) fxevent.Logger {
	fxLog := &fxevent.SlogLogger{Logger: log}
	fxLog.UseLogLevel(slog.LevelDebug)
	return fxLog
}

// newPool opens the database every library lives in.
//
// One pool for the process, because a pool is a property of the process. Which
// library a statement reads is decided per transaction, by the user it names.
func newPool(lc fx.Lifecycle, cfg config.Config) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open database: %w", err), db.Close())
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error { return db.Close() }})
	return db, nil
}

// newExtractor starts the PDF engine.
func newExtractor(lc fx.Lifecycle) (*pdf.Extractor, error) {
	extractor, err := pdf.New()
	if err != nil {
		return nil, fmt.Errorf("start the PDF engine: %w", err)
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error { return extractor.Close() }})
	return extractor, nil
}

// newFormats is the set of file formats an adapter can read.
func newFormats(extractor *pdf.Extractor) *adapter.Registry {
	return adapter.New(extractor)
}

// newLibraries builds each user's view of the database.
//
// Every route below asks it for the view of whoever a request named, which is
// why no route takes a service: a service belongs to one user, and the server
// is built before any request arrives.
func newLibraries(
	cfg config.Config,
	db *sql.DB,
	extractor *pdf.Extractor,
	formats *adapter.Registry,
) *library.Libraries {
	return library.New(db, extractor, formats, cfg.LibraryRoots, site.Options{}, time.Now)
}

// authenticated puts a handler behind the credential its mode asks for, and
// names the user it acts for.
//
// The owner is named inside the credential check, so a request that fails
// one never reaches a handler holding a user. Both modes answer with the
// built-in user, which is what makes them modes rather than two code paths:
// what differs is the credential, not who the library belongs to.
func authenticated(cfg config.Config, handler http.Handler) http.Handler {
	named := owner.Middleware(owner.Builtin, handler)
	if cfg.Auth == owner.None {
		// Refused above a loopback address, so reaching here means the
		// operator asked for an unauthenticated server on their own machine.
		return named
	}
	return httpx.RequireBearer(cfg.BearerToken, named)
}

// route is one mounted service: the path it answers on, and the handler.
type route struct {
	handler http.Handler
	path    string
}

// grouped tags a route constructor so that newMux receives every route as
// one group, which is what lets a service be added by writing its
// constructor and nothing else.
func grouped(constructor any) any {
	return fx.Annotate(constructor, fx.ResultTags(routeGroup))
}

// newMCPRoute is the endpoint Claude speaks to.
//
// Stateless, deliberately.
//
// In session mode the SDK holds session state in memory, so any restart of
// this process orphans every connected client: their next request carries an
// Mcp-Session-Id the new process has never seen and gets a 404. Clients are
// supposed to reinitialise on a 404, but not all do -- observed in practice
// as a client reporting the server "not responding" indefinitely after a
// restart it never noticed.
//
// Nothing here needs a session. Every tool is request/response; long ingest
// reports progress by polling ingest_status, not by server-initiated
// notifications. Giving up sessions costs nothing and makes a restart
// invisible to callers, which matters because this runs as a supervised
// service that is expected to be restarted.
//
// GET and DELETE return 405 in this mode; that is the SDK's contract, not a
// limitation we impose.
func newMCPRoute(
	cfg config.Config,
	libs *library.Libraries,
	ups uploads,
	log *slog.Logger,
) route {
	srv := mcpserver.New(mcpserver.Deps{
		Stores:       libs.Store,
		LibraryRoots: cfg.LibraryRoots,
		Log:          log,
		UploadLinks:  ups.issuer.Issue,
	})
	return route{
		path: "/mcp",
		handler: mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return srv },
			&mcp.StreamableHTTPOptions{Logger: log, Stateless: true},
		),
	}
}

// newIngestRoute wires the two halves an ingest needs, both of them the
// asking user's: the service that writes the library, and the sources that
// crawl through that user's cache.
func newIngestRoute(libs *library.Libraries) route {
	return mount(connectapi.NewServer(
		func(user string) (connectapi.Ingester, connectapi.Sources, error) {
			ingester, err := libs.Ingester(user)
			if err != nil {
				return nil, nil, err
			}
			sources, err := libs.Sources(user)
			if err != nil {
				return nil, nil, err
			}
			return ingester, sources, nil
		}))
}

func newDocumentRoute(libs *library.Libraries) route {
	return mount(connectapi.NewDocumentServer(
		func(user string) (connectapi.Documents, error) { return libs.Documents(user) },
		postgres.ErrNotFound))
}

func newJobRoute(libs *library.Libraries) route {
	return mount(connectapi.NewJobServer(
		func(user string) (connectapi.Jobs, error) { return libs.Jobs(user) },
		postgres.ErrNotFound))
}

// mount names what a handler constructor returns as a pair, so that a list of
// services reads as a list.
func mount(path string, handler http.Handler) route {
	return route{handler: handler, path: path}
}

// newMux puts every route behind the same token and origin checks, because a
// second way in with its own idea of who may use it is how one of them ends
// up wrong.
func newMux(
	routes []route,
	cfg config.Config,
	libs *library.Libraries,
	guard access,
	ups uploads,
	log *slog.Logger,
) *http.ServeMux {
	mux := http.NewServeMux()
	for _, r := range routes {
		mux.Handle(r.path, httpx.RequireAllowedOrigin(cfg.AllowedOrigins, guard.guard(r.handler)))
	}
	for _, r := range guard.public {
		mux.Handle(r.path, r.handler)
	}
	// A signed link is its own credential, and the page it serves posts to
	// itself from docsearch's own origin, so it sits outside both the guard
	// and the origin allowlist.
	mux.Handle(upload.LinkPrefix, ups.doors.Link())

	// Liveness: the process is up. Deliberately says nothing else.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	// Readiness: the database opens and the schema is present. Reports no
	// titles, paths or counts -- it is reachable without a token.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := libs.Ready(ctx); err != nil {
			// Names schema versions and table names only -- never document
			// titles, paths or counts. This endpoint has no auth.
			log.Error("readiness check failed", "error", err)
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ready")
	})
	return mux
}

// newServer binds the port during startup rather than after it.
//
// ListenAndServe in a goroutine reports a taken port to nobody: the process
// logs that it is listening and then exits. Listening first means a bind
// failure comes back from fx.Start as an error, before anything claims to be
// serving.
func newServer(
	lc fx.Lifecycle,
	mux *http.ServeMux,
	routes []route,
	cfg config.Config,
	log *slog.Logger,
	shutdown fx.Shutdowner,
) *http.Server {
	// Requests run on a context the server cancels itself at shutdown, once
	// the drain has had its time. Shutdown alone never cancels a request, so
	// a long ingest stream would otherwise outlive the grace period and be
	// cut off mid-write when the process exits.
	base, cancelRequests := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpx.LogRequests(log, mux),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listener, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.Addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
			}
			log.Info("docsearch-server listening",
				"addr", listener.Addr().String(),
				"roots", cfg.LibraryRoots, "allowed_origins", cfg.AllowedOrigins,
				"routes", mountedPaths(routes))
			go serveUntilStopped(srv, listener, log, shutdown)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return drain(ctx, srv, cancelRequests)
		},
	})
	return srv
}

// drainGrace is how long requests have to finish on their own at shutdown,
// out of shutdownGrace, before the ones still running are cancelled.
const drainGrace = shutdownGrace - 5*time.Second

// drain lets requests finish, then cancels any still running and waits for
// them to unwind. A cancelled ingest removes what it wrote, and a
// replacement leaves the version it was replacing untouched.
func drain(ctx context.Context, srv *http.Server, cancelRequests context.CancelFunc) error {
	patient, cancel := context.WithTimeout(ctx, drainGrace)
	defer cancel()
	err := srv.Shutdown(patient)
	cancelRequests()
	if err == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// mountedPaths is every mounted path, sorted, for the startup log.
func mountedPaths(routes []route) []string {
	paths := make([]string, 0, len(routes))
	for _, r := range routes {
		paths = append(paths, r.path)
	}
	slices.Sort(paths)
	return paths
}

// serveUntilStopped answers requests until OnStop shuts the server down.
//
// A listener that dies on its own has to bring the process down with it: a
// server answering nothing while its supervisor reads it as healthy is the
// worse failure, so it asks fx to stop rather than leaving the goroutine to
// end quietly.
func serveUntilStopped(
	srv *http.Server,
	listener net.Listener,
	log *slog.Logger,
	shutdown fx.Shutdowner,
) {
	err := srv.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	log.Error("stopped serving", "error", err)
	if err := shutdown.Shutdown(fx.ExitCode(1)); err != nil {
		log.Error("could not ask the server to stop", "error", err)
	}
}
