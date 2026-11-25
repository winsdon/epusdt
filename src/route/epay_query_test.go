package route

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/service"
	"github.com/labstack/echo/v4"
)

const epayQueryPath = "/payments/epay/v1/order/create-transaction/api.php"

type epayQueryRequest struct {
	method     string
	query      url.Values
	body       url.Values
	remoteAddr string
	header     map[string]string
}

func epayQueryValues(outTradeNo string) url.Values {
	return url.Values{
		"act":          {"order"},
		"pid":          {"1"},
		"key":          {testAPIToken},
		"out_trade_no": {outTradeNo},
	}
}

// doEPayQuery sends the request the way Sub2API does by default: the same
// parameters in both the URL query and the urlencoded body.
func doEPayQuery(e *echo.Echo, r epayQueryRequest) *httptest.ResponseRecorder {
	if r.method == "" {
		r.method = http.MethodPost
	}
	target := epayQueryPath
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}
	var req *http.Request
	if r.body != nil {
		req = httptest.NewRequest(r.method, target, strings.NewReader(r.body.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	} else {
		req = httptest.NewRequest(r.method, target, nil)
	}
	if r.remoteAddr != "" {
		req.RemoteAddr = r.remoteAddr
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func sub2apiQuery(e *echo.Echo, outTradeNo string) *httptest.ResponseRecorder {
	values := epayQueryValues(outTradeNo)
	return doEPayQuery(e, epayQueryRequest{query: values, body: values})
}

func decodeEPayQuery(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.HasPrefix(ct, echo.MIMEApplicationJSON) {
		t.Fatalf("Content-Type = %q, want JSON; body=%s", ct, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
	}
	return resp
}

func assertEPayQueryFailure(t *testing.T, rec *httptest.ResponseRecorder, wantHTTP int, wantMsg string) {
	t.Helper()
	if rec.Code != wantHTTP {
		t.Fatalf("HTTP %d, want %d; body=%s", rec.Code, wantHTTP, rec.Body.String())
	}
	resp := decodeEPayQuery(t, rec)
	if resp["code"] != float64(-1) || resp["msg"] != wantMsg {
		t.Fatalf("response = %v, want code -1 msg %q", resp, wantMsg)
	}
	for _, field := range []string{"trade_status", "status", "money", "trade_no", "out_trade_no", "pid"} {
		if _, ok := resp[field]; ok {
			t.Fatalf("error response leaks %q: %v", field, resp)
		}
	}
}

func mustLoadOrder(t *testing.T, tradeID string) *mdb.Orders {
	t.Helper()
	order, err := data.GetOrderInfoByTradeId(tradeID)
	if err != nil || order.ID == 0 {
		t.Fatalf("load order %s: %v", tradeID, err)
	}
	return order
}

func mustEPayAPIKey(t *testing.T, pid string) *mdb.ApiKey {
	t.Helper()
	row, err := data.GetEnabledApiKey(pid)
	if err != nil || row.ID == 0 {
		t.Fatalf("load api key %s: %v", pid, err)
	}
	return row
}

func TestEPayQueryOrderMatchesSuccessCallback(t *testing.T) {
	e := setupTestEnv(t)
	tradeID := mustCreateEPayOrder(t, e, "sub2_query_paid", "https://93.184.216.34/return", url.Values{"money": {"5000"}})

	pending := decodeEPayQuery(t, sub2apiQuery(e, "sub2_query_paid"))
	want := map[string]any{
		"code": float64(1), "msg": "success", "pid": "1",
		"out_trade_no": "sub2_query_paid", "trade_no": tradeID, "money": "5000.0000",
		"status": float64(0), "trade_status": "WAIT_BUYER_PAY",
	}
	for k, v := range want {
		if pending[k] != v {
			t.Fatalf("pending %s = %v, want %v; resp=%v", k, pending[k], v, pending)
		}
	}

	// Paid, with a coin amount different from the fiat amount and the
	// merchant callback not yet confirmed.
	if err := dao.Mdb.Model(&mdb.Orders{}).Where("trade_id = ?", tradeID).Updates(map[string]any{
		"status": mdb.StatusPaySuccess, "actual_amount": 746.25, "callback_confirm": mdb.CallBackConfirmNo,
	}).Error; err != nil {
		t.Fatal(err)
	}
	before := mustLoadOrder(t, tradeID)
	keyBefore := mustEPayAPIKey(t, "1")

	for _, method := range []string{http.MethodPost, http.MethodGet} {
		var rec *httptest.ResponseRecorder
		if method == http.MethodGet {
			rec = doEPayQuery(e, epayQueryRequest{method: method, query: epayQueryValues("sub2_query_paid")})
		} else {
			rec = sub2apiQuery(e, "sub2_query_paid")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", method, rec.Code, rec.Body.String())
		}
		paid := decodeEPayQuery(t, rec)
		if paid["code"] != float64(1) || paid["status"] != float64(1) || paid["trade_status"] != "TRADE_SUCCESS" {
			t.Fatalf("%s: paid order not reported as paid: %v", method, paid)
		}

		// The query must agree with the success callback Sub2API verified.
		apiKey, err := service.ResolveOrderApiKey(before)
		if err != nil {
			t.Fatal(err)
		}
		callback, err := service.BuildEPayResultParams(before, apiKey)
		if err != nil {
			t.Fatal(err)
		}
		// pid is the authenticated merchant's current PID by design, so
		// only the fields Sub2API cross-checks are compared here.
		for _, field := range []string{"trade_no", "out_trade_no", "money"} {
			if paid[field] != callback[field] {
				t.Fatalf("%s: %s = %v, callback has %q", method, field, paid[field], callback[field])
			}
		}
	}

	after := mustLoadOrder(t, tradeID)
	if after.Status != before.Status || after.CallBackConfirm != before.CallBackConfirm ||
		after.CallbackNum != before.CallbackNum || after.UpdatedAt.String() != before.UpdatedAt.String() {
		t.Fatalf("query modified the order: before=%+v after=%+v", before, after)
	}
	if keyAfter := mustEPayAPIKey(t, "1"); keyAfter.CallCount != keyBefore.CallCount {
		t.Fatalf("query modified api key usage: %d -> %d", keyBefore.CallCount, keyAfter.CallCount)
	}
}

func TestEPayQueryOrderStatusMapping(t *testing.T) {
	e := setupTestEnv(t)
	tradeID := mustCreateEPayOrder(t, e, "sub2_query_status", "https://93.184.216.34/return", nil)

	cases := []struct {
		status      int
		wantStatus  float64
		wantTrade   string
		description string
	}{
		{mdb.StatusWaitPay, 0, "WAIT_BUYER_PAY", "waiting for payment"},
		{mdb.StatusPaySuccess, 1, "TRADE_SUCCESS", "paid"},
		{mdb.StatusExpired, 0, "TRADE_CLOSED", "expired"},
		{mdb.StatusWaitSelect, 0, "WAIT_BUYER_PAY", "waiting for network selection"},
		{mdb.StatusConfirming, 0, "WAIT_BUYER_PAY", "confirming on chain"},
		{mdb.StatusManualReview, 0, "WAIT_BUYER_PAY", "manual review"},
		{99, 0, "WAIT_BUYER_PAY", "unknown status is never paid"},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			if err := dao.Mdb.Model(&mdb.Orders{}).Where("trade_id = ?", tradeID).Update("status", tc.status).Error; err != nil {
				t.Fatal(err)
			}
			resp := decodeEPayQuery(t, sub2apiQuery(e, "sub2_query_status"))
			if resp["code"] != float64(1) || resp["status"] != tc.wantStatus || resp["trade_status"] != tc.wantTrade {
				t.Fatalf("status %d: got %v", tc.status, resp)
			}
		})
	}
}

func TestEPayQueryOrderUsesParentForSwitchedOrders(t *testing.T) {
	e := setupTestEnv(t)
	parentTradeID := mustCreateEPayOrder(t, e, "sub2_query_parent", "https://93.184.216.34/return", nil)
	parent := mustLoadOrder(t, parentTradeID)

	child := mdb.Orders{
		TradeId: "query-child-trade", OrderId: "query-child-trade", ParentTradeId: parentTradeID,
		ApiKeyID: parent.ApiKeyID, PaymentType: mdb.PaymentTypeEpay, Status: mdb.StatusPaySuccess,
		Amount: parent.Amount, Currency: parent.Currency, CallBackConfirm: mdb.CallBackConfirmOk,
		BlockTransactionId: "0xchainhash",
	}
	if err := dao.Mdb.Create(&child).Error; err != nil {
		t.Fatal(err)
	}

	// Child paid but parent not yet settled: the merchant order is unpaid.
	resp := decodeEPayQuery(t, sub2apiQuery(e, "sub2_query_parent"))
	if resp["status"] != float64(0) || resp["trade_no"] != parentTradeID {
		t.Fatalf("unsettled parent reported as %v", resp)
	}

	if err := dao.Mdb.Model(parent).Updates(map[string]any{"status": mdb.StatusPaySuccess, "pay_by_sub_id": child.ID}).Error; err != nil {
		t.Fatal(err)
	}
	resp = decodeEPayQuery(t, sub2apiQuery(e, "sub2_query_parent"))
	if resp["trade_status"] != "TRADE_SUCCESS" || resp["trade_no"] != parentTradeID || resp["out_trade_no"] != "sub2_query_parent" {
		t.Fatalf("settled parent reported as %v", resp)
	}

	// A sub-order's own id is not a merchant order number.
	assertEPayQueryFailure(t, sub2apiQuery(e, child.OrderId), http.StatusOK, "order not found")
}

func TestEPayQueryOrderParameterRules(t *testing.T) {
	e := setupTestEnv(t)
	mustCreateEPayOrder(t, e, "sub2_query_params", "https://93.184.216.34/return", nil)
	valid := epayQueryValues("sub2_query_params")

	with := func(key string, values ...string) url.Values {
		out := url.Values{}
		for k, v := range valid {
			out[k] = append([]string(nil), v...)
		}
		out[key] = values
		return out
	}
	without := func(key string) url.Values {
		out := with(key)
		delete(out, key)
		return out
	}

	accepted := []struct {
		name string
		req  epayQueryRequest
	}{
		{"body only", epayQueryRequest{body: valid}},
		{"query only POST", epayQueryRequest{query: valid}},
		{"identical repeated value", epayQueryRequest{query: with("out_trade_no", "sub2_query_params", "sub2_query_params"), body: valid}},
		{"extra unknown parameters", epayQueryRequest{query: with("sign_type", "MD5"), body: valid}},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			rec := doEPayQuery(e, tc.req)
			if resp := decodeEPayQuery(t, rec); rec.Code != http.StatusOK || resp["code"] != float64(1) {
				t.Fatalf("HTTP %d %v", rec.Code, resp)
			}
		})
	}

	rejected := []struct {
		name string
		req  epayQueryRequest
		msg  string
	}{
		{"missing key", epayQueryRequest{query: without("key"), body: without("key")}, "invalid parameters"},
		{"missing out_trade_no", epayQueryRequest{body: without("out_trade_no")}, "invalid parameters"},
		{"empty act", epayQueryRequest{body: with("act", "")}, "invalid parameters"},
		{"query and body disagree", epayQueryRequest{query: valid, body: with("out_trade_no", "other")}, "invalid parameters"},
		{"repeated with different values", epayQueryRequest{body: with("pid", "1", "2")}, "invalid parameters"},
		{"unsupported act", epayQueryRequest{query: with("act", "refund"), body: with("act", "refund")}, "unsupported action"},
		{"wrong key", epayQueryRequest{body: with("key", "wrong-secret")}, "authentication failed"},
		{"unknown pid", epayQueryRequest{body: with("pid", "9999")}, "authentication failed"},
		{"oversized body", epayQueryRequest{body: with("padding", strings.Repeat("x", 70<<10))}, "invalid parameters"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			assertEPayQueryFailure(t, doEPayQuery(e, tc.req), http.StatusOK, tc.msg)
		})
	}
}

func TestEPayQueryOrderMerchantScopeAndAccess(t *testing.T) {
	e := setupTestEnv(t)
	tradeID := mustCreateEPayOrder(t, e, "sub2_query_scope", "https://93.184.216.34/return", nil)

	other := mustEPayAPIKey(t, testAPIToken)
	otherQuery := epayQueryValues("sub2_query_scope")
	otherQuery.Set("pid", other.Pid)
	t.Run("another merchant cannot see the order", func(t *testing.T) {
		assertEPayQueryFailure(t, doEPayQuery(e, epayQueryRequest{body: otherQuery}), http.StatusOK, "order not found")
	})

	t.Run("whitelist uses the direct peer, not forged X-Forwarded-For", func(t *testing.T) {
		if err := dao.Mdb.Model(&mdb.ApiKey{}).Where("pid = ?", "1").Update("ip_whitelist", "198.51.100.8").Error; err != nil {
			t.Fatal(err)
		}
		defer dao.Mdb.Model(&mdb.ApiKey{}).Where("pid = ?", "1").Update("ip_whitelist", "")
		values := epayQueryValues("sub2_query_scope")
		forged := epayQueryRequest{body: values, remoteAddr: "203.0.113.50:43120", header: map[string]string{echo.HeaderXForwardedFor: "198.51.100.8"}}
		assertEPayQueryFailure(t, doEPayQuery(e, forged), http.StatusOK, "authentication failed")
		allowed := epayQueryRequest{body: values, remoteAddr: "198.51.100.8:43120"}
		if resp := decodeEPayQuery(t, doEPayQuery(e, allowed)); resp["code"] != float64(1) {
			t.Fatalf("whitelisted peer rejected: %v", resp)
		}
	})

	t.Run("legacy order without merchant is not visible", func(t *testing.T) {
		legacy := mdb.Orders{TradeId: "legacy-query-trade", OrderId: "legacy_query_order", Status: mdb.StatusPaySuccess, Amount: 1, Currency: "CNY"}
		if err := dao.Mdb.Create(&legacy).Error; err != nil {
			t.Fatal(err)
		}
		assertEPayQueryFailure(t, sub2apiQuery(e, "legacy_query_order"), http.StatusOK, "order not found")
	})

	t.Run("disabled merchant", func(t *testing.T) {
		if err := dao.Mdb.Model(&mdb.ApiKey{}).Where("pid = ?", "1").Update("status", mdb.ApiKeyStatusDisable).Error; err != nil {
			t.Fatal(err)
		}
		defer dao.Mdb.Model(&mdb.ApiKey{}).Where("pid = ?", "1").Update("status", mdb.ApiKeyStatusEnable)
		assertEPayQueryFailure(t, sub2apiQuery(e, "sub2_query_scope"), http.StatusOK, "authentication failed")
	})

	t.Run("deleted order", func(t *testing.T) {
		if err := dao.Mdb.Where("trade_id = ?", tradeID).Delete(&mdb.Orders{}).Error; err != nil {
			t.Fatal(err)
		}
		assertEPayQueryFailure(t, sub2apiQuery(e, "sub2_query_scope"), http.StatusOK, "order not found")
	})
}
