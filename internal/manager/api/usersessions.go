package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Signed-in devices (#16): the browser sessions of an account, listed and
// signed out one by one. The flows live in internal/manager/auth
// (usersessions.go).

// UserSession is one signed-in browser session (a device). It never
// carries the session token.
type UserSession struct {
	ID            string    `json:"id" example:"0190a6e0-0000-7000-8000-000000000001"`
	Current       bool      `json:"current" doc:"The session of this request."`
	StaySignedIn  bool      `json:"staySignedIn" doc:"Signed in with Stay signed in (longer limits)."`
	CreatedAt     time.Time `json:"createdAt" doc:"When the device signed in."`
	LastSeenAt    time.Time `json:"lastSeenAt" doc:"The latest activity (recorded at most once a minute)."`
	IP            string    `json:"ip,omitempty" example:"203.0.113.7" doc:"Client IP of the latest activity."`
	UserAgent     string    `json:"userAgent,omitempty" example:"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0" doc:"The browser's User-Agent (at most 256 bytes)."`
	ExpiresAt     time.Time `json:"expiresAt" doc:"The session ends at this time at the latest."`
	IdleExpiresAt time.Time `json:"idleExpiresAt" doc:"The session ends at this time without further activity."`
}

func newUserSession(s domain.UserSession) UserSession {
	return UserSession{ID: s.ID, Current: s.Current, StaySignedIn: s.StaySignedIn, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt,
		IP: s.IP, UserAgent: s.UserAgent, ExpiresAt: s.ExpiresAt, IdleExpiresAt: s.IdleExpiresAt}
}

type userSessionListOutput struct{ Body Page[UserSession] }

func newUserSessionList(list []domain.UserSession) *userSessionListOutput {
	items := make([]UserSession, 0, len(list))
	for _, s := range list {
		items = append(items, newUserSession(s))
	}
	return &userSessionListOutput{Body: NewPage(items, "", Total(int64(len(items))))}
}

type mySessionIDInput struct {
	SessionID string `path:"sessionId" maxLength:"64" doc:"Session ID (from GET /api/v1/me/sessions)."`
}

type userSessionIDInput struct {
	UserID    string `path:"userId" maxLength:"64"`
	SessionID string `path:"sessionId" maxLength:"64" doc:"Session ID (from GET /api/v1/users/{userId}/sessions)."`
}

type sessionRevocationOutput struct {
	Body struct {
		Count int `json:"count" example:"2" doc:"How many devices were signed out."`
	}
}

func registerUserSessions(a huma.API, h *identityAPI) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-my-sessions", Method: http.MethodGet, Path: BasePath + "/me/sessions",
			Summary: "List my signed-in devices",
			Description: "The caller's browser sessions, most recently active first (a short list; no pagination): browser, IP, " +
				"sign-in and last activity. current marks this one.",
			Tags: []string{tagMe}, Security: cookieOnly,
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*userSessionListOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		list, err := svc.ListMySessions(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		return newUserSessionList(list), nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-my-session", Method: http.MethodDelete, Path: BasePath + "/me/sessions/{sessionId}",
			Summary: "Sign out one of my devices", DefaultStatus: http.StatusNoContent,
			Description: "The device is signed out at once and its open streams close. Signing out this device (current) " +
				"signs the caller out.",
			Tags: []string{tagMe}, Security: cookieOnly, Errors: []int{http.StatusNotFound},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *mySessionIDInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.RevokeMySession(ctx, in.SessionID); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-my-session-revocation", Method: http.MethodPost, Path: BasePath + "/me/session-revocations",
			Summary:     "Sign out my other devices",
			Description: "Signs out every device of the caller except this one; their open streams close. Returns how many.",
			Tags:        []string{tagMe}, Security: cookieOnly,
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*sessionRevocationOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		n, err := svc.RevokeMyOtherSessions(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		out := &sessionRevocationOutput{}
		out.Body.Count = n
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-user-sessions", Method: http.MethodGet, Path: BasePath + "/users/{userId}/sessions",
			Summary:     "List a user's signed-in devices",
			Description: "The account's browser sessions, most recently active first (no pagination). " + ownerOnly,
			Tags:        []string{tagUsers}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *userIDInput) (*userSessionListOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		list, err := svc.ListUserSessions(ctx, in.UserID)
		if err != nil {
			return nil, identityError(err)
		}
		return newUserSessionList(list), nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-user-session", Method: http.MethodDelete, Path: BasePath + "/users/{userId}/sessions/{sessionId}",
			Summary: "Sign out one of a user's devices", DefaultStatus: http.StatusNoContent,
			Description: "The device is signed out at once and its open streams close. " + ownerOnly,
			Tags:        []string{tagUsers}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *userSessionIDInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.RevokeUserSession(ctx, in.UserID, in.SessionID); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})
}
