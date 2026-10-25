package session

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/aapi"
	"github.com/yashiels/amazon-za-cli/internal/fault"
	"github.com/yashiels/amazon-za-cli/internal/persist"
)

type Session struct {
	ID        string    `json:"sessionId"`
	UBID      string    `json:"ubid"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func Cached(path string, now time.Time) (Session, bool, error) {
	var s Session
	err := persist.Read(path, &s)
	if errors.Is(err, os.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	return s, s.ID != "" && s.UBID != "" && !s.CreatedAt.After(now) && now.Before(s.ExpiresAt) && now.Before(s.CreatedAt.Add(7*24*time.Hour)), nil
}

func Bootstrap(ctx context.Context, t *aapi.Transport, path string, now time.Time) (Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.amazon.co.za/", nil)
	if err != nil {
		return Session{}, fault.New("session_bootstrap_failed", "bootstrap failed")
	}
	req.Header.Set("User-Agent", aapi.UserAgent)
	response, err := t.Send(req)
	if err != nil {
		var public *fault.Error
		if errors.As(err, &public) && (public.Code == "edge_blocked" || public.Code == "blocked_by_waf" || public.Code == "budget_exhausted") {
			return Session{}, err
		}
		return Session{}, fault.New("session_bootstrap_failed", "bootstrap failed")
	}
	s := Session{CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
	cookies := (&http.Response{Header: response.Header}).Cookies()
	for _, c := range cookies {
		if c.Name != "session-id" && c.Name != "ubid-acbza" {
			continue
		}
		if c.Name == "session-id" {
			s.ID = c.Value
		} else {
			s.UBID = c.Value
		}
		expiry := c.Expires
		if c.MaxAge > 0 {
			expiry = now.Add(time.Duration(c.MaxAge) * time.Second)
		}
		if c.MaxAge < 0 {
			expiry = now
		}
		if !expiry.IsZero() && expiry.Before(s.ExpiresAt) {
			s.ExpiresAt = expiry
		}
	}
	if s.ID == "" || s.UBID == "" || !s.ExpiresAt.After(now) {
		return Session{}, fault.New("session_bootstrap_failed", "missing session cookies")
	}
	if err := persist.Write(path, s); err != nil {
		return Session{}, err
	}
	return s, nil
}
