package comm

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/GMWalletApp/epusdt/middleware"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/response"
	"github.com/GMWalletApp/epusdt/model/service"
	"github.com/GMWalletApp/epusdt/util/log"
	"github.com/labstack/echo/v4"
)

const (
	epayQueryAction       = "order"
	epayQueryMaxBodyBytes = 64 << 10

	epayQueryMsgInvalidParams = "invalid parameters"
	epayQueryMsgUnsupported   = "unsupported action"
	epayQueryMsgAuthFailed    = "authentication failed"
	epayQueryMsgNotFound      = "order not found"
	epayQueryMsgUnavailable   = "service unavailable"
)

// epayQueryParamNames are the only parameters an EPay order query reads.
var epayQueryParamNames = []string{"act", "pid", "key", "out_trade_no"}

// EPayQueryOrder EPay 兼容查单
// @Summary      EPay compatible order query
// @Description  EPay/易支付 compatible read-only order query (act=order). Authenticates with pid + key (the merchant secret_key); sign, sign_type, JWT and GMPay HMAC are not used. The merchant IP whitelist applies.
// @Description  Parameters may be sent in the query string, the form body, or both. A parameter that appears with different values (across query and body, or repeated in one source) is rejected.
// @Description  Only the merchant's own top-level orders are visible. money is the merchant fiat order amount with 4 decimals, trade_no equals the success callback trade_no. status=1/trade_status=TRADE_SUCCESS only when the order is paid; unpaid orders still return code=1.
// @Description  Business errors return HTTP 200 with code=-1; service errors return HTTP 503 with code=-1. GET is also accepted.
// @Tags         Payment
// @Accept       x-www-form-urlencoded
// @Produce      json
// @Param        act formData string true "Fixed value: order"
// @Param        pid formData string true "Merchant PID"
// @Param        key formData string true "Merchant secret_key"
// @Param        out_trade_no formData string true "Merchant order ID"
// @Success      200 {object} response.EPayQueryOrderResponse
// @Failure      503 {object} response.EPayQueryErrorResponse "service unavailable"
// @Router       /payments/epay/v1/order/create-transaction/api.php [post]
// @Router       /payments/epay/v1/order/create-transaction/api.php [get]
func (c *BaseCommController) EPayQueryOrder(ctx echo.Context) error {
	ctx.Response().Header().Set("Cache-Control", "no-store")

	params, ok := readEPayQueryParams(ctx.Request(), ctx.Response())
	if !ok {
		return epayQueryFail(ctx, http.StatusOK, epayQueryMsgInvalidParams)
	}
	for _, name := range epayQueryParamNames {
		if params[name] == "" {
			return epayQueryFail(ctx, http.StatusOK, epayQueryMsgInvalidParams)
		}
	}
	if params["act"] != epayQueryAction {
		return epayQueryFail(ctx, http.StatusOK, epayQueryMsgUnsupported)
	}

	apiKey, err := authenticateEPayQuery(params["pid"], params["key"], ctx.RealIP())
	if err != nil {
		log.Sugar.Errorf("[epay-query] load api key failed pid=%s err=%v", params["pid"], err)
		return epayQueryFail(ctx, http.StatusServiceUnavailable, epayQueryMsgUnavailable)
	}
	if apiKey == nil {
		return epayQueryFail(ctx, http.StatusOK, epayQueryMsgAuthFailed)
	}

	result, err := service.QueryEPayOrder(apiKey, params["out_trade_no"])
	if errors.Is(err, service.ErrEPayOrderNotFound) {
		return epayQueryFail(ctx, http.StatusOK, epayQueryMsgNotFound)
	}
	if err != nil {
		log.Sugar.Errorf("[epay-query] query failed pid=%s out_trade_no=%s err=%v", params["pid"], params["out_trade_no"], err)
		return epayQueryFail(ctx, http.StatusServiceUnavailable, epayQueryMsgUnavailable)
	}
	return ctx.JSON(http.StatusOK, result)
}

// readEPayQueryParams collects the query parameters from the URL and the
// urlencoded body separately, because Echo's FormParams merges them and
// would hide conflicting duplicates. Any differing value is rejected.
func readEPayQueryParams(req *http.Request, w http.ResponseWriter) (map[string]string, bool) {
	if req.Body != nil {
		req.Body = http.MaxBytesReader(w, req.Body, epayQueryMaxBodyBytes)
	}
	if err := req.ParseForm(); err != nil {
		return nil, false
	}
	query := req.URL.Query()
	params := make(map[string]string, len(epayQueryParamNames))
	for _, name := range epayQueryParamNames {
		values := append(append([]string{}, query[name]...), req.PostForm[name]...)
		for _, v := range values {
			if v != values[0] {
				return nil, false
			}
		}
		if len(values) > 0 {
			params[name] = values[0]
		}
	}
	return params, true
}

// authenticateEPayQuery returns the enabled merchant for pid when key and
// the client IP match it, nil when authentication fails, and an error only
// when the merchant store cannot be read.
func authenticateEPayQuery(pid, key, clientIP string) (*mdb.ApiKey, error) {
	row, err := data.GetEnabledApiKey(pid)
	if err != nil {
		return nil, err
	}
	if row == nil || row.ID == 0 || row.SecretKey == "" {
		return nil, nil
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(row.SecretKey)) != 1 {
		return nil, nil
	}
	if !middleware.IsIPWhitelisted(row.IpWhitelist, clientIP) {
		return nil, nil
	}
	return row, nil
}

func epayQueryFail(ctx echo.Context, status int, msg string) error {
	return ctx.JSON(status, response.EPayQueryErrorResponse{Code: service.EPayQueryCodeFail, Msg: msg})
}
