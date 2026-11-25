package service

import (
	"errors"
	"fmt"
	"math"

	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/response"
)

const (
	EPayQueryCodeSuccess = 1
	EPayQueryCodeFail    = -1

	epayTradeSuccess  = "TRADE_SUCCESS"
	epayTradeWaitPay  = "WAIT_BUYER_PAY"
	epayTradeClosed   = "TRADE_CLOSED"
	epayQueryStatusOK = 1
)

// ErrEPayOrderNotFound means the order does not exist within the
// authenticated merchant's scope.
var ErrEPayOrderNotFound = errors.New("epay order not found")

// QueryEPayOrder answers an EPay api.php?act=order lookup for an
// authenticated merchant. It is read-only: the parent order row is the
// merchant-visible settlement result, and trade_no/money match the
// values BuildEPayResultParams sends in the success callback.
func QueryEPayOrder(apiKey *mdb.ApiKey, outTradeNo string) (*response.EPayQueryOrderResponse, error) {
	order, err := data.GetMerchantParentOrder(outTradeNo, apiKey.ID)
	if err != nil {
		return nil, fmt.Errorf("query merchant order: %w", err)
	}
	if order.ID == 0 {
		return nil, ErrEPayOrderNotFound
	}
	if math.IsNaN(order.Amount) || math.IsInf(order.Amount, 0) {
		return nil, fmt.Errorf("order %s has invalid amount", order.TradeId)
	}
	status, tradeStatus := epayQueryStatus(order.Status)
	return &response.EPayQueryOrderResponse{
		Code:        EPayQueryCodeSuccess,
		Msg:         "success",
		PID:         apiKey.Pid,
		OutTradeNo:  order.OrderId,
		TradeNo:     order.TradeId,
		Money:       FormatEPayMoney(order.Amount),
		Status:      status,
		TradeStatus: tradeStatus,
	}, nil
}

// epayQueryStatus maps internal order states onto EPay semantics. Only a
// settled order reports success; unknown states are never treated as paid.
func epayQueryStatus(status int) (int, string) {
	switch status {
	case mdb.StatusPaySuccess:
		return epayQueryStatusOK, epayTradeSuccess
	case mdb.StatusExpired:
		return 0, epayTradeClosed
	default:
		return 0, epayTradeWaitPay
	}
}
