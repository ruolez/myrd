package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

const (
	cookieName      = "rc_session"
	loginSessionTTL = 30 * 24 * time.Hour
	maxBodyBytes    = 1 << 20
)

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxLoginSession
)

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errf(status int, code, msg string) *apiError { return &apiError{status, code, msg} }

var (
	errUnauthorized = errf(http.StatusUnauthorized, "unauthorized", "sign in required")
	errForbidden    = errf(http.StatusForbidden, "forbidden", "not allowed")
	errNotFound     = errf(http.StatusNotFound, "not_found", "not found")
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handler adapts func(w, r) error so handlers can return *apiError.
type handler func(w http.ResponseWriter, r *http.Request) error

func (a *App) h(fn handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		var ae *apiError
		switch {
		case errors.As(err, &ae):
		case errors.Is(err, store.ErrNotFound):
			ae = errNotFound
		case errors.Is(err, store.ErrConflict):
			ae = errf(http.StatusConflict, "conflict", "already exists")
		default:
			a.log.Error("handler error", "path", r.URL.Path, "err", err)
			ae = errf(http.StatusInternalServerError, "internal", "internal error")
		}
		writeJSON(w, ae.Status, map[string]any{"error": map[string]string{"code": ae.Code, "message": ae.Message}})
	}
}

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return errf(http.StatusBadRequest, "bad_request", "missing body")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		return errf(http.StatusBadRequest, "bad_request", "invalid JSON body")
	}
	return nil
}

// decodeOptional accepts an empty body.
func decodeOptional(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return decode(r, v)
}

// clientIP is the rightmost X-Forwarded-For entry (added by our Caddy) when
// TRUST_PROXY is set, else the socket peer.
func (a *App) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

// originGuard rejects state-changing requests from a foreign Origin (CSRF).
func (a *App) originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if o := r.Header.Get("Origin"); o != "" && o != a.cfg.PublicOrigin {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "forbidden", "message": "cross-origin request refused"}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// loadUser attaches the cookie's user (if any) to the context.
func (a *App) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err == nil && c.Value != "" {
			sess, u, err := store.LoginSessionByHash(r.Context(), a.pool, security.SHA256(c.Value))
			if err == nil {
				if time.Since(sess.LastSeenAt) > 5*time.Minute {
					if err := store.SlideLoginSession(r.Context(), a.pool, sess.ID, loginSessionTTL); err != nil {
						a.log.Warn("slide session", "err", err)
					}
				}
				ctx := context.WithValue(r.Context(), ctxUser, u)
				ctx = context.WithValue(ctx, ctxLoginSession, sess)
				r = r.WithContext(ctx)
			} else if !errors.Is(err, store.ErrNotFound) {
				a.log.Error("load session", "err", err)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

func loginSessionFrom(r *http.Request) *store.LoginSession {
	s, _ := r.Context().Value(ctxLoginSession).(*store.LoginSession)
	return s
}

func requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r) == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "sign in required"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r)
		if u == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "sign in required"}})
			return
		}
		if !u.IsAdmin {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "forbidden", "message": "admin only"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode, MaxAge: int(loginSessionTTL.Seconds()),
	})
}

func (a *App) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// pathID returns the URL param as a UUID or a 404.
func pathID(r *http.Request, name string) (string, error) {
	v := r.PathValue(name)
	if v == "" {
		v = chiParam(r, name)
	}
	if _, err := uuid.Parse(v); err != nil {
		return "", errNotFound
	}
	return v, nil
}

func queryUUID(r *http.Request, name string) (string, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return "", nil
	}
	if _, err := uuid.Parse(v); err != nil {
		return "", errf(http.StatusBadRequest, "bad_request", name+" is not a valid id")
	}
	return v, nil
}

func pageLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}

func timeCursor(r *http.Request) (*time.Time, error) {
	v := r.URL.Query().Get("before")
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil, errf(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	return &t, nil
}

// page builds {items, next_before} where next is computed from the last item
// when the page is full.
func page[T any](items []T, limit int, cursor func(T) string) map[string]any {
	var next *string
	if len(items) == limit && limit > 0 {
		c := cursor(items[len(items)-1])
		next = &c
	}
	return map[string]any{"items": items, "next_before": next}
}

func (a *App) audit(ctx context.Context, ev store.Audit) {
	if err := store.InsertAudit(ctx, a.pool, ev); err != nil {
		a.log.Error("audit insert failed", "event", ev.Event, "err", err)
	}
}

func logAttrs(r *http.Request) []any {
	return []any{slog.String("method", r.Method), slog.String("path", r.URL.Path)}
}
