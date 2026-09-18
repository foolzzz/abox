package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	authpkg "agentbox/internal/auth"
	"agentbox/internal/domain"
	storepkg "agentbox/internal/store"

	"github.com/go-chi/chi/v5"
)

const (
	sessionCookieName  = "agentbox_session"
	sessionLifetime    = 7 * 24 * time.Hour
	loginFailureLimit  = 5
	loginFailureWindow = 15 * time.Minute
)

type loginFailure struct {
	attempts     int
	windowStart  time.Time
	blockedUntil time.Time
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if token := sessionToken(request); token != "" {
			user, err := s.store.UserBySession(request.Context(), authpkg.HashSessionToken(token), time.Now().UTC())
			if err == nil {
				next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), userContextKey{}, user)))
				return
			}
			if !errors.Is(err, storepkg.ErrNotFound) {
				s.logError("load account session", err)
				writeProblem(writer, http.StatusServiceUnavailable, "authentication_unavailable", "authentication is temporarily unavailable")
				return
			}
			s.clearSessionCookie(writer)
		}

		writeProblem(writer, http.StatusUnauthorized, "unauthenticated", "sign in with an AgentBox account")
	})
}

func (s *Server) requirePasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		user, ok := requestUser(request)
		if !ok {
			writeProblem(writer, http.StatusUnauthorized, "unauthenticated", "authentication is required")
			return
		}
		if user.MustChangePassword {
			writeProblem(writer, http.StatusForbidden, "password_change_required", "change the temporary password before continuing")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) handleLogin(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	attemptedUsername := strings.ToLower(strings.TrimSpace(body.Username))
	throttleKey := loginThrottleKey(request, attemptedUsername)
	if retryAfter := s.loginRetryAfter(throttleKey, time.Now()); retryAfter > 0 {
		writer.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Round(time.Second).Seconds())))
		writeProblem(writer, http.StatusTooManyRequests, "login_rate_limited", "too many failed login attempts; try again later")
		return
	}
	username, err := authpkg.NormalizeUsername(body.Username)
	if err != nil {
		authpkg.CheckPassword(s.dummyPasswordHash, body.Password)
		s.recordLoginFailure(throttleKey, time.Now())
		writeProblem(writer, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	credentials, err := s.store.FindAccountCredentials(request.Context(), username)
	if err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			authpkg.CheckPassword(s.dummyPasswordHash, body.Password)
			s.recordLoginFailure(throttleKey, time.Now())
			writeProblem(writer, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
			return
		}
		s.writeStoreError(writer, "find login account", err)
		return
	}
	if !authpkg.CheckPassword(credentials.PasswordHash, body.Password) {
		s.recordLoginFailure(throttleKey, time.Now())
		writeProblem(writer, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	s.clearLoginFailures(throttleKey)
	if err := s.issueSession(writer, request, credentials.User); err != nil {
		s.writeStoreError(writer, "create login session", err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, credentials.User)
}

func loginThrottleKey(request *http.Request, username string) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	return host + "\x00" + username
}

func (s *Server) loginRetryAfter(key string, now time.Time) time.Duration {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	failure, exists := s.loginFailures[key]
	if !exists {
		return 0
	}
	if now.Before(failure.blockedUntil) {
		return failure.blockedUntil.Sub(now)
	}
	if now.Sub(failure.windowStart) >= loginFailureWindow {
		delete(s.loginFailures, key)
	}
	return 0
}

func (s *Server) recordLoginFailure(key string, now time.Time) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	failure := s.loginFailures[key]
	if failure.windowStart.IsZero() || now.Sub(failure.windowStart) >= loginFailureWindow {
		failure = loginFailure{windowStart: now}
	}
	failure.attempts++
	if failure.attempts >= loginFailureLimit {
		failure.blockedUntil = now.Add(loginFailureWindow)
	}
	s.loginFailures[key] = failure
}

func (s *Server) clearLoginFailures(key string) {
	s.loginMu.Lock()
	delete(s.loginFailures, key)
	s.loginMu.Unlock()
}

func (s *Server) handleLogout(writer http.ResponseWriter, request *http.Request) {
	if token := sessionToken(request); token != "" {
		if err := s.store.DeleteSession(request.Context(), authpkg.HashSessionToken(token)); err != nil {
			s.writeStoreError(writer, "delete login session", err)
			return
		}
	}
	s.clearSessionCookie(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChangePassword(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	credentials, err := s.store.FindAccountCredentials(request.Context(), user.Login)
	if err != nil || credentials.User.ID != user.ID || !authpkg.CheckPassword(credentials.PasswordHash, body.CurrentPassword) {
		writeProblem(writer, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	passwordHash, err := authpkg.HashPassword(body.NewPassword)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	if err := s.store.ChangePassword(request.Context(), user, passwordHash); err != nil {
		s.writeStoreError(writer, "change account password", err)
		return
	}
	user.MustChangePassword = false
	if err := s.issueSession(writer, request, user); err != nil {
		s.writeStoreError(writer, "renew account session", err)
		return
	}
	writeJSON(writer, http.StatusOK, user)
}

func (s *Server) handleCreateAccount(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Role        string `json:"role"`
		Password    string `json:"password"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	username, err := authpkg.NormalizeUsername(body.Username)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" || len(displayName) > 128 {
		writeProblem(writer, http.StatusBadRequest, "invalid_display_name", "displayName must be between 1 and 128 characters")
		return
	}
	role := strings.ToLower(strings.TrimSpace(body.Role))
	if role != "admin" && role != "user" {
		writeProblem(writer, http.StatusBadRequest, "invalid_role", "role must be admin or user")
		return
	}
	passwordHash, err := authpkg.HashPassword(body.Password)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	member, err := s.store.CreateAccount(request.Context(), user, domain.CreateAccountInput{
		Username: username, DisplayName: displayName, Role: role, PasswordHash: passwordHash,
	})
	if err != nil {
		s.writeStoreError(writer, "create account", err)
		return
	}
	writeJSON(writer, http.StatusCreated, member)
}

func (s *Server) handleUpdateAccount(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	var body struct {
		DisplayName *string `json:"displayName"`
		Role        *string `json:"role"`
		Status      *string `json:"status"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	member, err := s.store.UpdateAccount(request.Context(), user, chiURLParam(request, "memberID"), domain.UpdateAccountInput{
		DisplayName: body.DisplayName, Role: body.Role, Status: body.Status,
	})
	if err != nil {
		s.writeStoreError(writer, "update account", err)
		return
	}
	writeJSON(writer, http.StatusOK, member)
}

func (s *Server) handleResetAccountPassword(writer http.ResponseWriter, request *http.Request) {
	user, _ := requestUser(request)
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	passwordHash, err := authpkg.HashPassword(body.Password)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	member, err := s.store.ResetAccountPassword(request.Context(), user, chiURLParam(request, "memberID"), passwordHash)
	if err != nil {
		s.writeStoreError(writer, "reset account password", err)
		return
	}
	writeJSON(writer, http.StatusOK, member)
}

func (s *Server) issueSession(writer http.ResponseWriter, request *http.Request, user domain.User) error {
	token, tokenHash, err := authpkg.NewSessionToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().UTC().Add(sessionLifetime)
	if err := s.store.CreateSession(request.Context(), user.ID, tokenHash, expiresAt); err != nil {
		return err
	}
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", Expires: expiresAt,
		MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, Secure: s.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (s *Server) clearSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func sessionToken(request *http.Request) string {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func chiURLParam(request *http.Request, name string) string {
	return chi.URLParam(request, name)
}
