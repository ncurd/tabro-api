package billingcenter

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type PaymentJob struct {
	SupportedTypes                                                            string
	Order                                                                     PaymentOrder
	OutTradeNo, ProviderInstanceID, ProviderKey, State, ProviderTransactionID string
	Checkout                                                                  *PaymentCheckout
}
type RefundJob struct {
	Refund                                                               PaymentRefund
	OutTradeNo, ProviderInstanceID, ProviderKey, State, ProviderRefundID string
}
type PaymentOutboxItem struct {
	EventID, EntityID, Kind string
	Payload                 json.RawMessage
}
type PaymentStore struct {
	DB         *sql.DB
	ProducerID string
}

func paymentHash(v any) (string, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}
func PaymentExternalID(producer, id string) string {
	h := sha256.Sum256([]byte(producer + "\x00" + id))
	return "tbc_" + hex.EncodeToString(h[:14])
}
func frozenPaymentOrder(order PaymentOrder) PaymentOrder {
	order.CheckoutClaimEventID = ""
	order.State = ""
	order.CheckoutState = ""
	order.ProviderInstanceID = ""
	order.ProviderTransactionID = ""
	order.PayURL = ""
	order.QRCode = ""
	return order
}

// Imported fulfillment is already part of the center's opening balance. This
// records only the provider reference needed to execute a later central refund.
func (s PaymentStore) AdoptOrder(ctx context.Context, job PaymentJob) (PaymentJob, error) {
	if job.Order.SourceSystem == "" || job.Order.SourceOrderID == "" || job.ProviderTransactionID == "" || job.OutTradeNo == "" {
		return PaymentJob{}, ErrConflict
	}
	h, encoded, err := paymentHash(frozenPaymentOrder(job.Order))
	if err != nil {
		return PaymentJob{}, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO billing_center_payment_jobs(producer_client_id,order_id,out_trade_no,frozen_order,frozen_fingerprint,provider_instance_id,provider_key,state,provider_transaction_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,'paid',$8) ON CONFLICT(producer_client_id,order_id) DO NOTHING`, s.ProducerID, job.Order.OrderID, job.OutTradeNo, string(encoded), h, job.ProviderInstanceID, job.ProviderKey, job.ProviderTransactionID)
	if err != nil {
		return PaymentJob{}, err
	}
	actual, err := s.Order(ctx, job.Order.OrderID)
	if err != nil {
		return actual, err
	}
	actualHash, _, err := paymentHash(frozenPaymentOrder(actual.Order))
	if err != nil || actualHash != h || actual.ProviderInstanceID != job.ProviderInstanceID || actual.ProviderTransactionID != job.ProviderTransactionID || actual.OutTradeNo != job.OutTradeNo {
		return PaymentJob{}, ErrConflict
	}
	return actual, nil
}
func (s PaymentStore) CreateOrder(ctx context.Context, order PaymentOrder, providerInstanceID, providerKey string, supportedTypes ...string) (PaymentJob, error) {
	if order.OrderID == "" || order.PaymentProducerID != s.ProducerID || providerInstanceID == "" || providerKey == "" {
		return PaymentJob{}, ErrConflict
	}
	h, b, err := paymentHash(frozenPaymentOrder(order))
	if err != nil {
		return PaymentJob{}, err
	}
	methods := ""
	if len(supportedTypes) > 0 {
		methods = supportedTypes[0]
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO billing_center_payment_jobs(producer_client_id,order_id,out_trade_no,frozen_order,frozen_fingerprint,provider_instance_id,provider_key,supported_types)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(producer_client_id,order_id) DO NOTHING`, s.ProducerID, order.OrderID, PaymentExternalID(s.ProducerID, order.OrderID), string(b), h, providerInstanceID, providerKey, methods)
	if err != nil {
		return PaymentJob{}, err
	}
	job, err := s.Order(ctx, order.OrderID)
	if err != nil {
		return job, err
	}
	existing, _, err := paymentHash(frozenPaymentOrder(job.Order))
	if err != nil || existing != h {
		return PaymentJob{}, ErrConflict
	}
	return job, nil
}
func (s PaymentStore) Order(ctx context.Context, id string) (PaymentJob, error) {
	var job PaymentJob
	var b, checkout []byte
	err := s.DB.QueryRowContext(ctx, `SELECT frozen_order,out_trade_no,provider_instance_id,provider_key,state,COALESCE(provider_transaction_id,''),checkout_json,supported_types FROM billing_center_payment_jobs WHERE producer_client_id=$1 AND order_id=$2`, s.ProducerID, id).Scan(&b, &job.OutTradeNo, &job.ProviderInstanceID, &job.ProviderKey, &job.State, &job.ProviderTransactionID, &checkout, &job.SupportedTypes)
	if errors.Is(err, sql.ErrNoRows) {
		return job, ErrNotFound
	}
	if err != nil {
		return job, err
	}
	if err = json.Unmarshal(b, &job.Order); err != nil {
		return job, err
	}
	if len(checkout) > 0 {
		job.Checkout = new(PaymentCheckout)
		err = json.Unmarshal(checkout, job.Checkout)
	}
	return job, err
}
func (s PaymentStore) OrderByExternalID(ctx context.Context, id string) (PaymentJob, error) {
	var orderID string
	err := s.DB.QueryRowContext(ctx, `SELECT order_id FROM billing_center_payment_jobs WHERE producer_client_id=$1 AND out_trade_no=$2`, s.ProducerID, id).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentJob{}, ErrNotFound
	}
	if err != nil {
		return PaymentJob{}, err
	}
	return s.Order(ctx, orderID)
}
func (s PaymentStore) ClaimCheckout(ctx context.Context, id string) (bool, error) {
	r, err := s.DB.ExecContext(ctx, `UPDATE billing_center_payment_jobs SET state='dispatching',updated_at=NOW() WHERE producer_client_id=$1 AND order_id=$2 AND state='pending'`, s.ProducerID, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (s PaymentStore) ReconcileOrder(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE billing_center_payment_jobs SET state='reconciliation_required',updated_at=NOW() WHERE producer_client_id=$1 AND order_id=$2 AND state IN ('pending','dispatching')`, s.ProducerID, id)
	return err
}

func (s PaymentStore) enqueuePayment(ctx context.Context, tx *sql.Tx, entity, kind, event string, request any) error {
	h, b, err := paymentHash(request)
	if err != nil {
		return err
	}
	if err = ValidatePersistentJSON(b); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_payment_outbox(producer_client_id,event_id,entity_id,kind,payload,fingerprint) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, s.ProducerID, event, entity, kind, string(b), h)
	if err != nil {
		return err
	}
	var actual, actualKind, actualEntity string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,kind,entity_id FROM billing_center_payment_outbox WHERE producer_client_id=$1 AND event_id=$2`, s.ProducerID, event).Scan(&actual, &actualKind, &actualEntity)
	if err != nil {
		return err
	}
	if actual != h || actualKind != kind || actualEntity != entity {
		return ErrConflict
	}
	return nil
}
func (s PaymentStore) CompleteCheckout(ctx context.Context, id string, checkout PaymentCheckout) error {
	return s.CompleteCheckoutWithReference(ctx, id, checkout, checkout.ProviderTransactionID)
}
func (s PaymentStore) CompleteCheckoutWithReference(ctx context.Context, id string, checkout PaymentCheckout, providerReference string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, instance string
	err = tx.QueryRowContext(ctx, `SELECT state,provider_instance_id FROM billing_center_payment_jobs WHERE producer_client_id=$1 AND order_id=$2 FOR UPDATE`, s.ProducerID, id).Scan(&state, &instance)
	if err != nil {
		return err
	}
	if instance != checkout.ProviderInstanceID || (state != "dispatching" && state != "ready" && state != "paid") {
		return ErrState
	}
	if err = s.enqueuePayment(ctx, tx, id, "checkout", checkout.EventID, checkout); err != nil {
		return err
	}
	b, err := json.Marshal(checkout)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_payment_jobs SET state=CASE WHEN state='paid' THEN state ELSE 'ready' END,checkout_json=$3,provider_transaction_id=COALESCE(NULLIF(provider_transaction_id,''),NULLIF($4,'')),updated_at=NOW() WHERE producer_client_id=$1 AND order_id=$2`, s.ProducerID, id, string(b), providerReference)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s PaymentStore) Paid(ctx context.Context, id string, paid PaymentPaid) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var orderBytes []byte
	err = tx.QueryRowContext(ctx, `SELECT state,frozen_order FROM billing_center_payment_jobs WHERE producer_client_id=$1 AND order_id=$2 FOR UPDATE`, s.ProducerID, id).Scan(&state, &orderBytes)
	if err != nil {
		return err
	}
	if state == "pending" || state == "cancelled" {
		return ErrState
	}
	var order PaymentOrder
	if err = json.Unmarshal(orderBytes, &order); err != nil {
		return err
	}
	if paid.ProviderTransactionID == "" || paid.PaymentCurrency != order.PaymentCurrency || !PaymentAmountsEqual(paid.PaymentAmount, order.PaymentAmount) {
		return ErrConflict
	}
	if err = s.enqueuePayment(ctx, tx, id, "paid", "paid:"+id, paid); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_payment_jobs SET state='paid',provider_transaction_id=$3,updated_at=NOW() WHERE producer_client_id=$1 AND order_id=$2`, s.ProducerID, id, paid.ProviderTransactionID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s PaymentStore) Cancelled(ctx context.Context, id string, cancelled PaymentCancelled) error {
	if !cancelled.ConfirmedNoPayment || cancelled.ProviderReference == "" {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM billing_center_payment_jobs WHERE producer_client_id=$1 AND order_id=$2 FOR UPDATE`, s.ProducerID, id).Scan(&state)
	if err != nil {
		return err
	}
	if state == "paid" || state == "pending" {
		return ErrState
	}
	if err = s.enqueuePayment(ctx, tx, id, "cancelled", cancelled.EventID, cancelled); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE billing_center_payment_jobs SET state='cancelled',updated_at=NOW() WHERE producer_client_id=$1 AND order_id=$2`, s.ProducerID, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s PaymentStore) CreateRefund(ctx context.Context, refund PaymentRefund, job PaymentJob) (RefundJob, error) {
	if refund.OrderID != job.Order.OrderID || refund.RefundID == "" {
		return RefundJob{}, ErrConflict
	}
	frozen := refund
	frozen.State = ""
	h, b, err := paymentHash(frozen)
	if err != nil {
		return RefundJob{}, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO billing_center_refund_jobs(producer_client_id,refund_id,order_id,frozen_refund,frozen_fingerprint,provider_instance_id,provider_key,out_trade_no) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, s.ProducerID, refund.RefundID, refund.OrderID, string(b), h, job.ProviderInstanceID, job.ProviderKey, job.OutTradeNo)
	if err != nil {
		return RefundJob{}, err
	}
	r, err := s.Refund(ctx, refund.RefundID)
	if err != nil {
		return r, err
	}
	existing, _, err := paymentHash(r.Refund)
	if err != nil || existing != h {
		return RefundJob{}, ErrConflict
	}
	return r, nil
}
func (s PaymentStore) Refund(ctx context.Context, id string) (RefundJob, error) {
	var r RefundJob
	var b []byte
	err := s.DB.QueryRowContext(ctx, `SELECT frozen_refund,out_trade_no,provider_instance_id,provider_key,state,COALESCE(provider_refund_id,'') FROM billing_center_refund_jobs WHERE producer_client_id=$1 AND refund_id=$2`, s.ProducerID, id).Scan(&b, &r.OutTradeNo, &r.ProviderInstanceID, &r.ProviderKey, &r.State, &r.ProviderRefundID)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r.Refund)
	return r, err
}
func (s PaymentStore) ClaimRefund(ctx context.Context, id string) (bool, error) {
	r, err := s.DB.ExecContext(ctx, `UPDATE billing_center_refund_jobs SET state='dispatching',updated_at=NOW() WHERE producer_client_id=$1 AND refund_id=$2 AND state='pending'`, s.ProducerID, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (s PaymentStore) MarkRefundPending(ctx context.Context, id, providerID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE billing_center_refund_jobs SET state=$3,provider_refund_id=NULLIF($4,''),updated_at=NOW() WHERE producer_client_id=$1 AND refund_id=$2 AND state IN ('dispatching','provider_pending','reconciliation_required')`, s.ProducerID, id, map[bool]string{true: "provider_pending", false: "reconciliation_required"}[providerID != ""], providerID)
	return err
}
func (s PaymentStore) CompleteRefund(ctx context.Context, id string, complete PaymentRefundComplete) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var b []byte
	err = tx.QueryRowContext(ctx, `SELECT state,frozen_refund FROM billing_center_refund_jobs WHERE producer_client_id=$1 AND refund_id=$2 FOR UPDATE`, s.ProducerID, id).Scan(&state, &b)
	if err != nil {
		return err
	}
	if state == "pending" {
		return ErrState
	}
	var refund PaymentRefund
	if err = json.Unmarshal(b, &refund); err != nil {
		return err
	}
	if complete.ProviderRefundID == "" || complete.PaymentCurrency != refund.PaymentCurrency || !PaymentAmountsEqual(complete.PaymentAmount, refund.PaymentAmount) {
		return ErrConflict
	}
	if err = s.enqueuePayment(ctx, tx, id, "refund_complete", "refund:"+id, complete); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_center_refund_jobs SET state=$3,provider_refund_id=$4,updated_at=NOW() WHERE producer_client_id=$1 AND refund_id=$2`, s.ProducerID, id, map[bool]string{true: "succeeded", false: "failed"}[complete.Succeeded], complete.ProviderRefundID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s PaymentStore) PendingPaymentEvents(ctx context.Context) ([]PaymentOutboxItem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT event_id,entity_id,kind,payload FROM billing_center_payment_outbox WHERE producer_client_id=$1 AND delivered_at IS NULL ORDER BY created_at,event_id LIMIT 30`, s.ProducerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PaymentOutboxItem{}
	for rows.Next() {
		var item PaymentOutboxItem
		if err = rows.Scan(&item.EventID, &item.EntityID, &item.Kind, &item.Payload); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s PaymentStore) PaymentEventResult(ctx context.Context, id string, delivered bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE billing_center_payment_outbox SET attempts=attempts+1,delivered_at=CASE WHEN $3 THEN NOW() ELSE delivered_at END,last_error_code=CASE WHEN $3 THEN NULL ELSE 'delivery_pending' END WHERE producer_client_id=$1 AND event_id=$2 AND delivered_at IS NULL`, s.ProducerID, id, delivered)
	return err
}
