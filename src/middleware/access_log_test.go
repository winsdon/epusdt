package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRedactRequestURI(t *testing.T) {
	cases := []struct {
		in, want string
		changed  bool
	}{
		{"/api.php", "/api.php", false},
		{"/api.php?act=order&out_trade_no=1", "/api.php?act=order&out_trade_no=1", false},
		{"/api.php?act=order&key=s3cret&pid=1", "/api.php?act=order&key=***&pid=1", true},
		{"/x?k%65y=s3cret&KEY=s3cret", "/x?k%65y=***&KEY=***", true},
		{"/x?secret_key=s3cret&monkey=ok", "/x?secret_key=***&monkey=ok", true},
		{"/x?key", "/x?key=***", true},
		{"/x?act=order;key=s3cret&pid=1", "/x?act=order;key=***&pid=1", true},
	}
	for _, tc := range cases {
		got, changed := redactRequestURI(tc.in)
		if got != tc.want || changed != tc.changed {
			t.Errorf("redactRequestURI(%q) = %q, %v; want %q, %v", tc.in, got, changed, tc.want, tc.changed)
		}
	}
}

func TestAccessLoggerRedactsKeyButHandlerSeesIt(t *testing.T) {
	var out bytes.Buffer
	e := echo.New()
	e.Use(AccessLogger(&out))
	var seenKey string
	e.POST("/payments/epay/v1/order/create-transaction/api.php", func(c echo.Context) error {
		seenKey = c.QueryParam("key")
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/payments/epay/v1/order/create-transaction/api.php?act=order&pid=1&key=s3cret-merchant-key&out_trade_no=o1", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	if seenKey != "s3cret-merchant-key" {
		t.Fatalf("handler saw key %q", seenKey)
	}
	logged := out.String()
	if strings.Contains(logged, "s3cret-merchant-key") {
		t.Fatalf("access log leaked key: %s", logged)
	}
	if !strings.Contains(logged, `key=***`) || !strings.Contains(logged, "out_trade_no=o1") {
		t.Fatalf("access log lost the redacted uri: %s", logged)
	}
}
