package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// telegramAPIError is the structured failure of a Bot API call: the HTTP
// status, the decoded error envelope and the raw body. Callers branch on its
// fields (see parseTelegramRetryAfter, isTelegramRateLimitError,
// isRetryableUploadError) instead of searching the message text; the message
// itself keeps each method's historical wording.
type telegramAPIError struct {
	Method string
	// StatusCode and Status are the HTTP status of the response.
	StatusCode int
	Status     string
	// Decoded reports whether the body was a JSON Bot API envelope. The
	// fields below it are only meaningful when it is true.
	Decoded     bool
	ErrorCode   int
	Description string
	// RetryAfter is parameters.retry_after in seconds, 0 when absent.
	RetryAfter int
	// Body is the trimmed raw response body.
	Body string

	msg   string
	cause error
}

func (e *telegramAPIError) Error() string { return e.msg }

// Unwrap exposes the JSON decode failure of an HTTP 200 response whose body
// was not a Bot API envelope.
func (e *telegramAPIError) Unwrap() error { return e.cause }

// retryAfter returns how long Telegram asked us to wait. It prefers
// parameters.retry_after and otherwise looks for the same hint in the
// response body ("Too Many Requests: retry after N").
func (e *telegramAPIError) retryAfter() (time.Duration, bool) {
	if e == nil {
		return 0, false
	}
	if e.RetryAfter > 0 {
		return time.Duration(e.RetryAfter) * time.Second, true
	}
	return retryAfterFromText(e.Body)
}

func (e *telegramAPIError) hasCode(code int) bool {
	return e != nil && (e.StatusCode == code || e.ErrorCode == code)
}

func (e *telegramAPIError) rateLimited() bool {
	if _, ok := e.retryAfter(); ok {
		return true
	}
	return e.hasCode(http.StatusTooManyRequests)
}

// retryable reports whether repeating the call can succeed: rate limits,
// gateway failures, a response cut off mid-body, or a transient condition
// named in the response.
func (e *telegramAPIError) retryable() bool {
	if e == nil {
		return false
	}
	if e.rateLimited() || e.hasCode(http.StatusBadGateway) {
		return true
	}
	if errors.Is(e.cause, io.EOF) || errors.Is(e.cause, io.ErrUnexpectedEOF) {
		return true
	}
	return containsRetryableHint(e.Body)
}

func asTelegramAPIError(err error) (*telegramAPIError, bool) {
	var apiErr *telegramAPIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr, true
	}
	return nil, false
}

func retryAfterFromText(text string) (time.Duration, bool) {
	for _, re := range []*regexp.Regexp{retryAfterJSONRe, retryAfterTextRe} {
		matches := re.FindStringSubmatch(text)
		if len(matches) < 2 {
			continue
		}
		sec, convErr := strconv.Atoi(matches[1])
		if convErr != nil || sec <= 0 {
			continue
		}
		return time.Duration(sec) * time.Second, true
	}
	return 0, false
}

var retryableErrorHints = []string{
	"context deadline exceeded",
	"client.timeout exceeded",
	"tls handshake timeout",
	"connection reset by peer",
	"use of closed network connection",
	"broken pipe",
	"read/write on closed pipe",
	"i/o timeout",
	"eof",
	"unexpected eof",
	"bad gateway",
	"temporarily unavailable",
	"too many requests",
	"file_parts_invalid",
}

func containsRetryableHint(text string) bool {
	lower := strings.ToLower(text)
	for _, hint := range retryableErrorHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// telegramErrorFormat renders the message of a failed call. Every Bot API
// method keeps its own wording because callers and tests match on it.
type telegramErrorFormat func(e *telegramAPIError) string

// telegramErrorText builds the usual format: one wording for a non-200
// response, one for an HTTP 200 envelope with ok=false. An HTTP 200 body that
// is not JSON reports the decode error itself.
func telegramErrorText(httpFailure, notOK func(e *telegramAPIError) string) telegramErrorFormat {
	return func(e *telegramAPIError) string {
		switch {
		case e.StatusCode != http.StatusOK:
			return httpFailure(e)
		case !e.Decoded:
			return e.cause.Error()
		default:
			return notOK(e)
		}
	}
}

// telegramFailedBody is "<method> failed: <body>", the wording most methods
// use for a non-200 response.
func telegramFailedBody(operation string) func(e *telegramAPIError) string {
	return func(e *telegramAPIError) string {
		return operation + " failed: " + e.Body
	}
}

// telegramErrorDescription is "<method> error: <description>".
func telegramErrorDescription(operation string) func(e *telegramAPIError) string {
	return func(e *telegramAPIError) string {
		return operation + " error: " + e.Description
	}
}

// telegramErrorWithParameters is the ok=false wording that also spells out
// parameters.retry_after (see telegramAPIResponseErrorText).
func telegramErrorWithParameters(operation string) func(e *telegramAPIError) string {
	return func(e *telegramAPIError) string {
		return telegramAPIResponseErrorText(operation, e.Description, e.RetryAfter)
	}
}

type telegramEnvelope struct {
	OK          bool                  `json:"ok"`
	Result      json.RawMessage       `json:"result"`
	ErrorCode   int                   `json:"error_code,omitempty"`
	Description string                `json:"description,omitempty"`
	Parameters  apiResponseParameters `json:"parameters,omitempty"`
}

// telegramNoResult is the result type for calls whose result is not used.
type telegramNoResult = json.RawMessage

// callTelegramJSON posts payload as JSON to a Bot API method and decodes the
// result into T. Transport failures are returned as they are; any response
// that is not an HTTP 200 ok=true envelope becomes a *telegramAPIError worded
// by format, and its retry_after (if any) is applied to the send limiter.
func callTelegramJSON[T any](ctx context.Context, b *TelegramBot, method string, payload any, format telegramErrorFormat) (T, error) {
	var zero T
	body, err := json.Marshal(payload)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL(method), bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil && resp.StatusCode == http.StatusOK {
		return zero, err
	}
	return decodeTelegramResponse[T](b, method, resp.StatusCode, resp.Status, responseBody, format)
}

// decodeTelegramResponse turns a Bot API response into a typed result or a
// *telegramAPIError. It is shared by callTelegramJSON and the streaming
// multipart uploads, which build their own requests.
func decodeTelegramResponse[T any](b *TelegramBot, method string, statusCode int, status string, responseBody []byte, format telegramErrorFormat) (T, error) {
	var result T
	envelope := telegramEnvelope{}
	decodeErr := json.NewDecoder(bytes.NewReader(responseBody)).Decode(&envelope)
	if statusCode == http.StatusOK && decodeErr == nil && envelope.OK {
		if len(envelope.Result) > 0 {
			if err := json.Unmarshal(envelope.Result, &result); err != nil {
				var zero T
				return zero, err
			}
		}
		return result, nil
	}
	apiErr := &telegramAPIError{
		Method:     method,
		StatusCode: statusCode,
		Status:     status,
		Decoded:    decodeErr == nil,
		Body:       strings.TrimSpace(string(responseBody)),
	}
	if decodeErr == nil {
		apiErr.ErrorCode = envelope.ErrorCode
		apiErr.Description = envelope.Description
		apiErr.RetryAfter = envelope.Parameters.RetryAfter
	} else if statusCode == http.StatusOK {
		apiErr.cause = decodeErr
	}
	apiErr.msg = format(apiErr)
	b.noteTelegramRateLimit(apiErr)
	var zero T
	return zero, apiErr
}
