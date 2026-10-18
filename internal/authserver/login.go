package authserver

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/bamsammich/docsearch/internal/password"
)

// signInPage is what the page shows: the app asking, where its code goes,
// and whatever went wrong last time.
type signInPage struct {
	RequestID string
	Client    string
	SendsTo   string
	Username  string
	Message   string
	Open      bool
}

// login is the page a person signs in on. GET shows the form; POST checks
// the password and, once it is right, sends the browser back through the
// provider, which issues the code.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.showLogin(w, r, r.URL.Query().Get("authRequestID"), "", "", http.StatusOK)
	case http.MethodPost:
		s.signIn.ServeHTTP(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) showLogin(
	w http.ResponseWriter,
	r *http.Request,
	id, username, message string,
	status int,
) {
	page := signInPage{RequestID: id, Username: username, Message: message}
	request, err := s.storage.current(r.Context(), id)
	switch {
	case errors.Is(err, ErrExpired), errors.Is(err, errNotFound):
		page.Message = ErrExpired.Error()
		status = http.StatusBadRequest
	case err != nil:
		s.log.ErrorContext(r.Context(), "read sign-in request", "err", err)
		http.Error(w, "the sign-in page could not be shown", http.StatusInternalServerError)
		return
	default:
		page.Open = true
		page.Client, page.SendsTo = s.describe(r, request)
	}
	s.render(w, r, page, status)
}

// describe names the app asking and the host its code goes to, the two
// things a person needs to notice a request they did not start.
func (s *Server) describe(r *http.Request, request Request) (string, string) {
	name := ""
	if registered, found, err := s.store.Client(
		r.Context(),
		request.ClientID,
	); err == nil &&
		found {
		name = registered.Name
	}
	sendsTo := request.RedirectURI
	if parsed, err := url.Parse(request.RedirectURI); err == nil && parsed.Host != "" {
		sendsTo = parsed.Host
	}
	return name, sendsTo
}

// checkSignIn checks a posted password and, once it is right, sends the
// browser back through the provider.
func (s *Server) checkSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "sign in from docsearch's own page", http.StatusForbidden)
		return
	}
	id, username := r.PostFormValue("authRequestID"), r.PostFormValue("username")
	if _, err := s.storage.current(r.Context(), id); err != nil {
		s.showLogin(w, r, id, username, "", http.StatusBadRequest)
		return
	}
	userID, err := s.accounts.Authenticate(r.Context(), username, r.PostFormValue("password"))
	switch {
	case errors.Is(err, password.ErrSignInRefused):
		s.showLogin(w, r, id, username, err.Error(), http.StatusUnauthorized)
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "sign in", "err", err)
		s.showLogin(w, r, id, username, "signing in failed on the server; try again",
			http.StatusInternalServerError)
		return
	}
	if err := s.store.CompleteRequest(r.Context(), id, userID, s.now()); err != nil {
		s.log.ErrorContext(r.Context(), "complete sign-in", "err", err)
		http.Error(w, "the sign-in could not be finished", http.StatusInternalServerError)
		return
	}
	ctx := op.ContextWithIssuer(r.Context(), s.provider.IssuerFromRequest(r))
	http.Redirect(w, r, op.AuthCallbackURL(s.provider)(ctx, id), http.StatusFound)
}

// sameOrigin refuses a form posted from another site. A browser names the
// origin of a cross-site POST, so a missing header is a client that is not a
// browser, which has no cookie to abuse anyway.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	issuer, err := url.Parse(s.cfg.Issuer)
	if err != nil {
		return false
	}
	return origin == issuer.Scheme+"://"+issuer.Host
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page signInPage, status int) {
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set(
		"Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'",
	)
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := signInTemplate.Execute(w, page); err != nil {
		s.log.ErrorContext(r.Context(), "render sign-in page", "err", err)
	}
}

var signInTemplate = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in to docsearch</title>
<style>
:root { color-scheme: light dark; --ink: #1d1f1e; --muted: #5b625e; --ground: #f6f6f3;
        --card: #ffffff; --rule: #cfd3cf; --accent: #2f6f5b; --alert: #9b3a2a; }
@media (prefers-color-scheme: dark) { :root { --ink: #e6e8e6; --muted: #a2aaa5; --ground: #161817;
        --card: #1e2120; --rule: #343a37; --accent: #79c7a9; --alert: #e59c8c; } }
body { margin: 0; padding: 48px max(16px, env(safe-area-inset-left)); background: var(--ground);
       color: var(--ink); font: 16px/1.5 system-ui, -apple-system, sans-serif; }
main { max-width: 24rem; margin: 0 auto; display: grid; gap: 16px; }
h1 { font-size: 1.4rem; margin: 0; text-wrap: balance; }
p { margin: 0; color: var(--muted); }
form { display: grid; gap: 12px; padding: 20px; background: var(--card);
       border: 1px solid var(--rule); border-radius: 8px; }
label { display: grid; gap: 4px; font-size: 0.9rem; }
input { font: inherit; padding: 8px 10px; border: 1px solid var(--rule); border-radius: 6px;
        background: var(--ground); color: var(--ink); }
input:focus-visible, button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
button { justify-self: start; padding: 8px 16px; border: 0; border-radius: 6px;
         background: var(--accent); color: var(--card); font: inherit; cursor: pointer; }
.alert { color: var(--alert); }
strong { color: var(--ink); }
</style></head><body><main>
<h1>Sign in to docsearch</h1>
{{if .Open}}
<p>An app is asking to read and add to your library.{{if .Client}} It calls itself
<strong>{{.Client}}</strong>, a name any app can choose, so docsearch has not
checked it.{{end}} Your browser goes back to <strong>{{.SendsTo}}</strong> once you
sign in. If you did not just connect an app, or you do not recognise that address,
close this page.</p>
<form method="post" action="/login">
<input type="hidden" name="authRequestID" value="{{.RequestID}}">
<label for="username">Username
<input id="username" name="username" autocomplete="username" autocapitalize="none" value="{{.Username}}" required autofocus></label>
<label for="password">Password
<input id="password" name="password" type="password" autocomplete="current-password" required></label>
{{if .Message}}<p class="alert" role="alert">{{.Message}}</p>{{end}}
<button type="submit">Sign in</button>
</form>
{{else}}
<p class="alert" role="alert">{{.Message}}</p>
{{end}}
</main></body></html>
`))
