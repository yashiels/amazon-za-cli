package fault

import "errors"

type Error struct {
	Message string `json:"error"`
	Code    string `json:"code"`
	Status  int    `json:"status,omitempty"`
	Exit    int    `json:"-"`
}

func (e *Error) Error() string        { return e.Message }
func New(code, message string) *Error { return &Error{Message: message, Code: code, Exit: 1} }
func Usage() *Error {
	return &Error{Message: "invalid command arguments; run amazon --help", Code: "usage", Exit: 4}
}
func Public(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		messages := map[string]string{
			"edge_blocked":             "Amazon edge blocked this request; stop and try later",
			"usage":                    "invalid command arguments; run amazon --help",
			"blocked_by_waf":           "Amazon blocked this request; stop and try later",
			"session_bootstrap_failed": "could not establish an anonymous session; run amazon doctor",
			"aapi_rejected":            "Amazon rejected the signed request; run amazon doctor",
			"budget_exhausted":         "request attempt limit reached",
			"http_error":               "Amazon returned an unsuccessful HTTP status",
			"network_error":            "request failed or timed out",
			"redirect_refused":         "Amazon returned a redirect; redirects are disabled",
			"response_too_large":       "response exceeded the size limit",
			"invalid_response":         "Amazon returned an invalid response",
			"unsafe_request":           "request is outside the read-only allowlist",
			"storage_error":            "could not safely access local storage",
			"locked":                   "watchlist is locked by another process",
			"watch_full":               "watchlist has reached its 100 item limit",
			"price_unavailable":        "a current ZAR price is unavailable",
			"not_found":                "product could not be found",
		}
		message, ok := messages[e.Code]
		if ok {
			return &Error{Message: message, Code: e.Code, Status: e.Status, Exit: e.Exit}
		}
	}
	return New("runtime_error", "unexpected failure")
}
