package middleware

import (
	"io"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	echoMiddleware "github.com/labstack/echo/v4/middleware"
)

const redactedValue = "***"

// sensitiveQueryParams are query parameters that carry merchant secrets,
// e.g. the EPay order query sends key=<secret_key> in the URL.
var sensitiveQueryParams = []string{"key", "secret_key"}

// AccessLogger is Echo's default access logger with secret query values
// redacted from the logged uri. output may be nil for the default stdout.
//
// Echo v4.6.0 logs req.RequestURI and has no custom tag hook, so the
// logger receives a shallow request copy whose RequestURI is redacted;
// routing and handlers keep reading the untouched req.URL.
func AccessLogger(output io.Writer) echo.MiddlewareFunc {
	config := echoMiddleware.DefaultLoggerConfig
	if output != nil {
		config.Output = output
	}
	logger := echoMiddleware.LoggerWithConfig(config)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		logged := logger(next)
		return func(c echo.Context) error {
			req := c.Request()
			if redacted, changed := redactRequestURI(req.RequestURI); changed {
				clone := req.WithContext(req.Context())
				clone.RequestURI = redacted
				c.SetRequest(clone)
			}
			return logged(c)
		}
	}
}

// redactRequestURI masks the values of sensitive query parameters while
// keeping every other byte of the request URI as received.
func redactRequestURI(requestURI string) (string, bool) {
	path, rawQuery, hasQuery := strings.Cut(requestURI, "?")
	if !hasQuery {
		return requestURI, false
	}
	changed := false
	pairs := strings.Split(rawQuery, "&")
	for i, pair := range pairs {
		// Go rejects ';' separators, but legacy clients still send them; a
		// rejected pair must not leave its secret in the log either.
		segments := strings.Split(pair, ";")
		for j, segment := range segments {
			if redacted, ok := redactQuerySegment(segment); ok {
				segments[j] = redacted
				changed = true
			}
		}
		pairs[i] = strings.Join(segments, ";")
	}
	if !changed {
		return requestURI, false
	}
	return path + "?" + strings.Join(pairs, "&"), true
}

func redactQuerySegment(segment string) (string, bool) {
	rawName, _, _ := strings.Cut(segment, "=")
	name, err := url.QueryUnescape(rawName)
	if err != nil {
		name = rawName
	}
	if !isSensitiveQueryParam(name) {
		return segment, false
	}
	return rawName + "=" + redactedValue, true
}

func isSensitiveQueryParam(name string) bool {
	for _, sensitive := range sensitiveQueryParams {
		if strings.EqualFold(name, sensitive) {
			return true
		}
	}
	return false
}
