package billingcenter

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func PaymentAmountsEqual(a, b Decimal) bool {
	if !decimalPattern.MatchString(string(a)) || !decimalPattern.MatchString(string(b)) {
		return false
	}
	x, ok := new(big.Rat).SetString(string(a))
	if !ok {
		return false
	}
	y, ok := new(big.Rat).SetString(string(b))
	return ok && x.Cmp(y) == 0
}
func PaymentMinorUnits(amount Decimal) (int64, error) {
	if !decimalPattern.MatchString(string(amount)) {
		return 0, ErrConflict
	}
	r, ok := new(big.Rat).SetString(string(amount))
	if !ok || r.Sign() <= 0 {
		return 0, ErrConflict
	}
	r.Mul(r, big.NewRat(100, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, ErrConflict
	}
	return r.Num().Int64(), nil
}

type PaymentAuthority interface {
	PaymentWork(context.Context, int) (PaymentWork, error)
	PaymentOrder(context.Context, string) (PaymentOrder, error)
	ClaimPaymentCheckout(context.Context, string, PaymentClaim) (PaymentOrder, error)
	CompletePaymentCheckout(context.Context, string, PaymentCheckout) (PaymentOrder, error)
	ConfirmPaymentPaid(context.Context, string, PaymentPaid) (PaymentOrder, error)
	ConfirmPaymentCancelled(context.Context, string, PaymentCancelled) (PaymentOrder, error)
	CompletePaymentRefund(context.Context, string, PaymentRefundComplete) (PaymentRefund, error)
}
type PaymentProviderSelection struct {
	InstanceID, Key, SupportedTypes string
	Provider                        payment.Provider
}
type PaymentProviderResolver interface {
	Select(context.Context, PaymentOrder) (PaymentProviderSelection, error)
	Resolve(context.Context, string, string) (payment.Provider, error)
	// Adopt resolves only an already fulfilled, frozen source order and checks
	// its exact amount/transaction against Auth's imported financial evidence.
	Adopt(context.Context, PaymentOrder) (PaymentJob, error)
}
type PaymentBridge struct {
	Store                                 PaymentStore
	Authority                             PaymentAuthority
	Providers                             PaymentProviderResolver
	CallbackBaseURL, AccountCenterBaseURL string
}

func (b *PaymentBridge) Tick(ctx context.Context) error {
	var failures []error
	if err := b.Deliver(ctx); err != nil {
		failures = append(failures, err)
	}
	work, err := b.Authority.PaymentWork(ctx, 20)
	if err != nil {
		return err
	}
	for _, order := range work.Orders {
		if err = b.Checkout(ctx, order); err != nil {
			failures = append(failures, err)
		}
	}
	for _, refund := range work.Refunds {
		if err = b.Refund(ctx, refund); err != nil {
			failures = append(failures, err)
		}
	}
	if err = b.PollPayments(ctx); err != nil {
		failures = append(failures, err)
	}
	if err = b.Deliver(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
func (b *PaymentBridge) Checkout(ctx context.Context, order PaymentOrder) error {
	if order.PaymentProducerID != b.Store.ProducerID || !strings.HasPrefix(order.OrderID, "center-order-") {
		return ErrConflict
	}
	if _, err := PaymentMinorUnits(order.PaymentAmount); err != nil {
		return err
	}
	job, err := b.Store.Order(ctx, order.OrderID)
	var selection PaymentProviderSelection
	if errors.Is(err, ErrNotFound) {
		selection, err = b.Providers.Select(ctx, order)
		if err != nil {
			return err
		}
		job, err = b.Store.CreateOrder(ctx, order, selection.InstanceID, selection.Key, selection.SupportedTypes)
	}
	if err != nil {
		return err
	}
	expectedHash, _, err := paymentHash(frozenPaymentOrder(job.Order))
	if err != nil {
		return err
	}
	actualHash, _, err := paymentHash(frozenPaymentOrder(order))
	if err != nil || actualHash != expectedHash {
		return ErrConflict
	}
	if job.State != "pending" {
		return nil
	}
	claimed, err := b.Store.ClaimCheckout(ctx, order.OrderID)
	if err != nil || !claimed {
		return err
	}
	claim, err := b.Authority.ClaimPaymentCheckout(ctx, order.OrderID, PaymentClaim{EventID: "checkout:" + order.OrderID})
	if err != nil {
		return err
	}
	if claim.OrderID != order.OrderID || claim.PaymentProducerID != b.Store.ProducerID || claim.CheckoutState != "dispatched" || claim.CheckoutClaimEventID != "checkout:"+order.OrderID {
		return ErrConflict
	}
	claimHash, _, err := paymentHash(frozenPaymentOrder(claim))
	if err != nil || claimHash != expectedHash {
		return ErrConflict
	}
	prov, err := b.Providers.Resolve(ctx, job.ProviderInstanceID, job.ProviderKey)
	if err != nil {
		return err
	}
	notify := strings.TrimRight(b.CallbackBaseURL, "/") + paymentNotifyPath(job.ProviderKey)
	if paymentNotifyPath(job.ProviderKey) == "" {
		return ErrConflict
	}
	returnURL := strings.TrimRight(b.AccountCenterBaseURL, "/") + "/Identity/Account/Manage/BillingPayments?orderId=" + url.QueryEscape(order.OrderID)
	response, err := prov.CreatePayment(ctx, payment.CreatePaymentRequest{OrderID: job.OutTradeNo, Amount: string(order.PaymentAmount), Currency: order.PaymentCurrency, PaymentType: order.PaymentMethod, Subject: order.ProductName, NotifyURL: notify, ReturnURL: returnURL, CancelURL: returnURL, InstanceSubMethods: job.SupportedTypes})
	if err != nil {
		return err
	} // Durable dispatching remains unknown; never blindly Create again.
	if response == nil || response.PayURL == "" && response.QRCode == "" {
		return ErrState
	}
	checkout := PaymentCheckout{EventID: "checkout-ready:" + order.OrderID, ProviderInstanceID: job.ProviderInstanceID, PayURL: response.PayURL, QRCode: response.QRCode}
	// Keep a checkout session ID local for provider querying. It is not necessarily
	// the canonical completed-payment transaction ID used for money deduplication.
	return b.Store.CompleteCheckoutWithReference(ctx, order.OrderID, checkout, response.TradeNo)
}
func paymentNotifyPath(provider string) string {
	switch provider {
	case "stripe", "alipay", "wxpay", "easypay":
		return "/api/v1/payment/webhook/" + provider
	}
	return ""
}

func (b *PaymentBridge) Notify(ctx context.Context, n *payment.PaymentNotification, providerKey string) error {
	if n == nil || n.Status != payment.NotificationStatusSuccess && n.Status != payment.NotificationStatusPaid {
		return nil
	}
	job, err := b.Store.OrderByExternalID(ctx, n.OrderID)
	if err != nil {
		return err
	}
	if providerKey != job.ProviderKey || n.Currency != job.Order.PaymentCurrency || !PaymentAmountsEqual(Decimal(n.ExactAmount), job.Order.PaymentAmount) {
		return ErrConflict
	}
	prov, err := b.Providers.Resolve(ctx, job.ProviderInstanceID, job.ProviderKey)
	if err != nil {
		return err
	}
	trade := n.TradeNo
	if normalizer, ok := prov.(payment.PaymentReferenceResolver); ok {
		trade, err = normalizer.CanonicalPaymentReference(ctx, trade)
		if err != nil {
			return err
		}
	}
	if trade == "" {
		return ErrConflict
	}
	return b.Store.Paid(ctx, job.Order.OrderID, PaymentPaid{EventID: "paid:" + job.Order.OrderID, ProviderTransactionID: trade, PaymentCurrency: n.Currency, PaymentAmount: job.Order.PaymentAmount})
}
func (b *PaymentBridge) PollPayments(ctx context.Context) error {
	rows, err := b.Store.DB.QueryContext(ctx, `SELECT order_id FROM billing_center_payment_jobs WHERE producer_client_id=$1 AND state IN ('ready','dispatching','reconciliation_required') ORDER BY updated_at LIMIT 20`, b.Store.ProducerID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		job, e := b.Store.Order(ctx, id)
		if e != nil {
			return e
		}
		prov, e := b.Providers.Resolve(ctx, job.ProviderInstanceID, job.ProviderKey)
		if e != nil {
			continue
		}
		reference := job.OutTradeNo
		if job.ProviderKey == "stripe" {
			reference = job.ProviderTransactionID
			if reference == "" {
				continue
			}
		}
		status, e := prov.QueryOrder(ctx, reference)
		if e != nil || status == nil {
			continue
		}
		if status.Status == payment.ProviderStatusPaid || status.Status == payment.ProviderStatusSuccess {
			e = b.Notify(ctx, &payment.PaymentNotification{OrderID: job.OutTradeNo, TradeNo: status.TradeNo, ExactAmount: status.ExactAmount, Currency: status.Currency, Status: payment.NotificationStatusSuccess}, job.ProviderKey)
			if e != nil {
				return e
			}
		} else if status.ConfirmedUnpaidClosed {
			if e = b.Store.Cancelled(ctx, id, PaymentCancelled{EventID: "cancelled:" + id, ProviderReference: reference, ConfirmedNoPayment: true}); e != nil {
				return e
			}
		}
	}
	return nil
}
func (b *PaymentBridge) Refund(ctx context.Context, refund PaymentRefund) error {
	if _, err := PaymentMinorUnits(refund.PaymentAmount); err != nil {
		return err
	}
	job, err := b.Store.Order(ctx, refund.OrderID)
	if errors.Is(err, ErrNotFound) && refund.SourceSystem != "" {
		order, e := b.Authority.PaymentOrder(ctx, refund.OrderID)
		if e != nil {
			return e
		}
		job, err = b.Providers.Adopt(ctx, order)
	}
	if err != nil {
		return err
	}
	if job.State != "paid" || job.Order.PaymentCurrency != refund.PaymentCurrency || job.ProviderTransactionID != refund.ProviderTransactionID {
		return ErrConflict
	}
	r, err := b.Store.CreateRefund(ctx, refund, job)
	if err != nil {
		return err
	}
	prov, err := b.Providers.Resolve(ctx, r.ProviderInstanceID, r.ProviderKey)
	if err != nil {
		return err
	}
	request := payment.RefundRequest{RefundID: PaymentExternalID(b.Store.ProducerID, refund.RefundID), OrderID: r.OutTradeNo, TradeNo: refund.ProviderTransactionID, Amount: string(refund.PaymentAmount), Reason: "Account center refund " + refund.RefundID}
	if r.State == "provider_pending" || r.State == "dispatching" || r.State == "reconciliation_required" {
		if query, ok := prov.(payment.RefundRequestStatusProvider); ok {
			response, e := query.QueryRefundRequest(ctx, request)
			if e != nil {
				return e
			}
			return b.finishRefund(ctx, r, response)
		}
		if r.ProviderRefundID == "" {
			return nil
		}
		query, ok := prov.(payment.RefundStatusProvider)
		if !ok {
			return nil
		}
		response, e := query.QueryRefund(ctx, r.ProviderRefundID)
		if e != nil {
			return e
		}
		return b.finishRefund(ctx, r, response)
	}
	if r.State != "pending" {
		return nil
	}
	claimed, err := b.Store.ClaimRefund(ctx, refund.RefundID)
	if err != nil || !claimed {
		return err
	}
	response, err := prov.Refund(ctx, request)
	if err != nil {
		return err
	} // Never free held funds on an ambiguous provider error.
	return b.finishRefund(ctx, r, response)
}
func (b *PaymentBridge) finishRefund(ctx context.Context, r RefundJob, response *payment.RefundResponse) error {
	if response == nil || response.RefundID == "" {
		return ErrState
	}
	if response.Status != payment.ProviderStatusSuccess && response.Status != payment.ProviderStatusFailed {
		return b.Store.MarkRefundPending(ctx, r.Refund.RefundID, response.RefundID)
	}
	return b.Store.CompleteRefund(ctx, r.Refund.RefundID, PaymentRefundComplete{EventID: "refund:" + r.Refund.RefundID, ProviderRefundID: response.RefundID, PaymentCurrency: r.Refund.PaymentCurrency, PaymentAmount: r.Refund.PaymentAmount, Succeeded: response.Status == payment.ProviderStatusSuccess})
}
func (b *PaymentBridge) Deliver(ctx context.Context) error {
	items, err := b.Store.PendingPaymentEvents(ctx)
	if err != nil {
		return err
	}
	var failures []error
	blocked := make(map[string]bool)
	for _, item := range items {
		if blocked[item.EntityID] {
			continue
		}
		var e error
		switch item.Kind {
		case "checkout":
			var payload PaymentCheckout
			if e = json.Unmarshal(item.Payload, &payload); e == nil {
				var result PaymentOrder
				result, e = b.Authority.CompletePaymentCheckout(ctx, item.EntityID, payload)
				if e == nil && (result.OrderID != item.EntityID || result.CheckoutState != "ready") {
					e = ErrConflict
				}
			}
		case "paid":
			var payload PaymentPaid
			if e = json.Unmarshal(item.Payload, &payload); e == nil {
				var result PaymentOrder
				result, e = b.Authority.ConfirmPaymentPaid(ctx, item.EntityID, payload)
				if e == nil && (result.OrderID != item.EntityID || result.ProviderTransactionID != payload.ProviderTransactionID || (result.State != "paid" && result.State != "refunded")) {
					e = ErrConflict
				}
			}
		case "refund_complete":
			var payload PaymentRefundComplete
			if e = json.Unmarshal(item.Payload, &payload); e == nil {
				var result PaymentRefund
				result, e = b.Authority.CompletePaymentRefund(ctx, item.EntityID, payload)
				if e == nil && (result.RefundID != item.EntityID || result.State != map[bool]string{true: "succeeded", false: "failed"}[payload.Succeeded]) {
					e = ErrConflict
				}
			}
		case "cancelled":
			var payload PaymentCancelled
			if e = json.Unmarshal(item.Payload, &payload); e == nil {
				var result PaymentOrder
				result, e = b.Authority.ConfirmPaymentCancelled(ctx, item.EntityID, payload)
				if e == nil && (result.OrderID != item.EntityID || result.State != "cancelled") {
					e = ErrConflict
				}
			}
		default:
			e = ErrConflict
		}
		if err = b.Store.PaymentEventResult(ctx, item.EventID, e == nil); err != nil {
			return err
		}
		if e != nil {
			blocked[item.EntityID] = true
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (b *PaymentBridge) Run(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := b.Tick(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
