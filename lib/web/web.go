package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/gorilla/schema"
	"github.com/gorilla/sessions"
	_ "github.com/motemen/go-loghttp/global"
	"github.com/pkg/errors"

	"github.com/motemen/prchecklist"
	"github.com/motemen/prchecklist/lib/usecase"
)

var (
	sessionSecret = os.Getenv("PRCHECKLIST_SESSION_SECRET")
	behindProxy   = os.Getenv("PRCHECKLIST_BEHIND_PROXY") != ""
)

const sessionName = "s"

const (
	sessionKeyOAuthState = "oauthState"
	sessionKeyGitHubUser = "githubUser"
)

const htmlContent = `<!DOCTYPE html>
<meta name=viewport content="width=device-width">
<body><div id="main"></div></body>
<script src="/js/bundle.js"></script>
`

var sessionStore sessions.Store

func init() {
	flag.StringVar(&sessionSecret, "session-secret", sessionSecret, "session secret (PRCHECKLIST_SESSION_SECRET)")
	flag.BoolVar(&behindProxy, "behind-proxy", behindProxy, "prchecklist is behind a reverse proxy (PRCHECKLIST_BEHIND_PROXY)")

	gob.Register(&prchecklist.GitHubUser{})
}

type GitHubGateway interface {
	AuthCodeURL(state string) string
	AuthenticateUser(ctx context.Context, code string) (*prchecklist.GitHubUser, error)
}

type Web struct {
	app    *usecase.Usecase
	github GitHubGateway
}

func New(app *usecase.Usecase, github GitHubGateway) *Web {
	return &Web{
		app:    app,
		github: github,
	}
}

func (web *Web) Handler() http.Handler {
	cookieStore := sessions.NewCookieStore([]byte(sessionSecret))
	cookieStore.Options = &sessions.Options{
		Path:     "/",
		HttpOnly: true,
	}
	sessionStore = cookieStore

	router := mux.NewRouter()
	router.Handle("/", httpHandler(web.handleIndex))
	router.Handle("/auth", httpHandler(web.handleAuth))
	router.Handle("/auth/callback", httpHandler(web.handleAuthCallback))
	router.Handle("/auth/clear", httpHandler(web.handleAuthClear))
	router.Handle("/api/me", httpHandler(web.handleAPIMe))
	router.Handle("/api/checklist", httpHandler(web.handleAPIChecklist))
	router.Handle("/api/check", httpHandler(web.handleAPICheck)).Methods("PUT", "DELETE")
	router.Handle("/{owner}/{repo}/pull/{number}", httpHandler(web.handleChecklist))
	router.Handle("/{owner}/{repo}/pull/{number}/{stage}", httpHandler(web.handleChecklist))
	router.PathPrefix("/js/").Handler(httpHandler(web.handleStaticJS))

	if behindProxy {
		return handlers.ProxyHeaders(router)
	}

	return router
}

type httpError int

func (he httpError) Error() string {
	return http.StatusText(int(he))
}

type httpHandler func(w http.ResponseWriter, req *http.Request) error

func (h httpHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	err := h(w, req)
	if err != nil {
		log.Printf("ServeHTTP: %s (%+v)", err, err)

		status := http.StatusInternalServerError
		if he, ok := err.(httpError); ok {
			status = int(he)
		}

		http.Error(w, fmt.Sprintf("%+v", err), status)
	}
}

func renderJSON(w http.ResponseWriter, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
	return nil
}

func (web *Web) handleAuth(w http.ResponseWriter, req *http.Request) error {
	sess, _ := sessionStore.Get(req, sessionName)

	state, err := makeRandomString()
	if err != nil {
		return err
	}

	sess.Values[sessionKeyOAuthState] = state
	err = sessionStore.Save(req, w, sess)
	if err != nil {
		return err
	}

	http.Redirect(w, req, web.github.AuthCodeURL(state), http.StatusFound)

	return nil
}

func makeRandomString() (string, error) {
	buf := make([]byte, 16)
	_, err := rand.Read(buf)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (web *Web) handleIndex(w http.ResponseWriter, req *http.Request) error {
	fmt.Fprint(w, htmlContent)
	return nil
}

func (web *Web) handleAuthCallback(w http.ResponseWriter, req *http.Request) error {
	sess, err := sessionStore.Get(req, sessionName)
	if err != nil {
		return errors.Wrapf(err, "sessionStore.Get")
	}

	state := req.URL.Query().Get("state")
	log.Printf("%#v", sess.Values)
	if state != sess.Values[sessionKeyOAuthState] {
		log.Printf("%v != %v", state, sess.Values[sessionKeyOAuthState])
		return httpError(http.StatusBadRequest)
	}

	delete(sess.Values, sessionKeyOAuthState)

	ctx := prchecklist.RequestContext(req)

	code := req.URL.Query().Get("code")
	user, err := web.github.AuthenticateUser(ctx, code)
	if err != nil {
		return err
	}

	sess.Values[sessionKeyGitHubUser] = *user

	err = web.app.AddUser(ctx, *user)
	if err != nil {
		return err
	}

	err = sess.Save(req, w)
	if err != nil {
		return err
	}

	http.Redirect(w, req, "/", http.StatusFound)

	return nil
}

func (web *Web) handleAuthClear(w http.ResponseWriter, req *http.Request) error {
	http.SetCookie(w, &http.Cookie{
		Name:    sessionName,
		Path:    "/",
		Expires: time.Now().Add(-1 * time.Hour),
	})

	http.Redirect(w, req, "/", http.StatusFound)

	return nil
}

func (web *Web) getAuthInfo(w http.ResponseWriter, req *http.Request) (*prchecklist.GitHubUser, error) {
	sess, err := sessionStore.Get(req, sessionName)
	if err != nil {
		// FIXME
		// return nil, err
		return nil, nil
	}

	v, ok := sess.Values[sessionKeyGitHubUser]
	if !ok {
		return nil, nil
	}

	user, ok := v.(*prchecklist.GitHubUser)
	if !ok || user.Token == nil {
		delete(sess.Values, sessionKeyGitHubUser)
		return nil, sess.Save(req, w)
	}

	return user, nil
}

func (web *Web) handleAPIMe(w http.ResponseWriter, req *http.Request) error {
	u, _ := web.getAuthInfo(w, req)
	return renderJSON(w, u)
}

func (web *Web) handleAPIChecklist(w http.ResponseWriter, req *http.Request) error {
	u, err := web.getAuthInfo(w, req)
	if err != nil {
		return err
	}
	if u == nil {
		return httpError(http.StatusForbidden)
	}

	type inQuery struct {
		Owner  string
		Repo   string
		Number int
		Stage  string
	}

	var in inQuery
	err = schema.NewDecoder().Decode(&in, req.URL.Query())
	if err != nil {
		return err
	}
	if in.Stage == "" {
		in.Stage = "default"
	}

	ctx := prchecklist.RequestContext(req)
	ctx = context.WithValue(ctx, prchecklist.ContextKeyHTTPClient, u.HTTPClient(ctx))

	cl, err := web.app.GetChecklist(ctx, prchecklist.ChecklistRef{
		Owner:  in.Owner,
		Repo:   in.Repo,
		Number: in.Number,
		Stage:  in.Stage,
	})
	if err != nil {
		return err
	}

	return renderJSON(w, &prchecklist.ChecklistResponse{
		Checklist: cl,
		Me:        u,
	})
}

func (web *Web) handleAPICheck(w http.ResponseWriter, req *http.Request) error {
	u, err := web.getAuthInfo(w, req)
	if err != nil {
		return err
	}
	if u == nil {
		return httpError(http.StatusForbidden)
	}

	type inQuery struct {
		Owner         string
		Repo          string
		Number        int
		Stage         string
		FeatureNumber int
	}

	if err := req.ParseForm(); err != nil {
		return err
	}

	var in inQuery
	err = schema.NewDecoder().Decode(&in, req.Form)
	if err != nil {
		return err
	}
	if in.Stage == "" {
		in.Stage = "default"
	}

	clRef := prchecklist.ChecklistRef{
		Owner:  in.Owner,
		Repo:   in.Repo,
		Number: in.Number,
		Stage:  in.Stage,
	}
	ctx := prchecklist.RequestContext(req)
	ctx = context.WithValue(ctx, prchecklist.ContextKeyHTTPClient, u.HTTPClient(ctx))

	log.Printf("handleAPICheck: %s %+v", req.Method, in)

	switch req.Method {
	case "PUT":
		checklist, err := web.app.AddCheck(ctx, clRef, in.FeatureNumber, *u)
		if err != nil {
			return err
		}
		return renderJSON(w, &prchecklist.ChecklistResponse{
			Checklist: checklist,
			Me:        u,
		})

	case "DELETE":
		checklist, err := web.app.RemoveCheck(ctx, clRef, in.FeatureNumber, *u)
		if err != nil {
			return err
		}
		return renderJSON(w, &prchecklist.ChecklistResponse{
			Checklist: checklist,
			Me:        u,
		})

	default:
		return httpError(http.StatusMethodNotAllowed)
	}
}

func (web *Web) handleChecklist(w http.ResponseWriter, req *http.Request) error {
	fmt.Fprint(w, htmlContent)
	return nil
}

func (web *Web) handleStaticJS(w http.ResponseWriter, req *http.Request) error {
	path := "static" + req.URL.Path

	b, err := Asset(path)
	if err != nil {
		http.NotFound(w, req)
		return nil
	}

	w.Write(b)
	return nil
}
