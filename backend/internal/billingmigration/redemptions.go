package billingmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
)

type SourceRedeemTransfer struct {
	SourceSystem      string  `json:"source_system"`
	SourceEventID     string  `json:"source_event_id"`
	Code              string  `json:"code"`
	CodeSHA256        string  `json:"code_sha256"`
	Kind              string  `json:"kind"`
	Value             string  `json:"value"`
	GroupID           *string `json:"group_id"`
	ValidityDays      int     `json:"validity_days"`
	OperatorReference string  `json:"operator_reference"`
	TransferID        string  `json:"transfer_id"`
}
type FrozenRedeemReceipt struct {
	SourceSystem      string `json:"source_system"`
	SourceEventID     string `json:"source_event_id"`
	TransferID        string `json:"transfer_id"`
	CodeSHA256        string `json:"code_sha256"`
	Kind              string `json:"kind"`
	Value             string `json:"value"`
	OperatorReference string `json:"operator_reference"`
	State             string `json:"state"`
}
type RedeemExport struct {
	Transfer      SourceRedeemTransfer `json:"transfer"`
	FrozenReceipt FrozenRedeemReceipt  `json:"frozen_receipt"`
}

// The caller writes only to a private, exclusively created file. SQL/log/audit
// evidence contain a digest, never the usable code. A retry reproduces the file.
func (s Source) ExportRedeem(ctx context.Context, id int64, transferID, reference string) ([]byte, string, error) {
	if id <= 0 || transferID == "" || reference == "" {
		return nil, "", errors.New("source code ID, stable transfer ID and operator reference required")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	t := SourceRedeemTransfer{SourceSystem: "tabro-api", SourceEventID: strconv.FormatInt(id, 10), TransferID: transferID, OperatorReference: reference}
	var state string
	var usedBy sql.NullInt64
	var group sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT code,type,value::text,group_id::text,COALESCE(validity_days,0),status,used_by FROM redeem_codes WHERE id=$1 FOR UPDATE`, id).Scan(&t.Code, &t.Kind, &t.Value, &group, &t.ValidityDays, &state, &usedBy)
	if err != nil {
		return nil, "", err
	}
	if state != "unused" || usedBy.Valid || t.Kind != "balance" {
		return nil, "", errors.New("only unused balance codes support direct transfer; subscription codes require a reviewed entitlement mapping")
	}
	if group.Valid {
		t.GroupID = &group.String
	}
	digest := sha256.Sum256([]byte(t.Code))
	t.CodeSHA256 = hex.EncodeToString(digest[:])
	receipt := FrozenRedeemReceipt{t.SourceSystem, t.SourceEventID, t.TransferID, t.CodeSHA256, t.Kind, t.Value, t.OperatorReference, "fenced"}
	evidence, err := json.Marshal(receipt)
	if err != nil {
		return nil, "", err
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT evidence FROM billing_center_redeem_authority WHERE source_code_id=$1`, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		// Advance the row's MVCC version before installing the immutable guard. An
		// old repeatable-read redemption snapshot must fail rather than double spend.
		if _, err = tx.ExecContext(ctx, `UPDATE redeem_codes SET billing_authority_revision=billing_authority_revision+1 WHERE id=$1`, id); err != nil {
			return nil, "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO billing_center_redeem_authority(source_code_id,transfer_id,code_sha256,evidence) VALUES($1,$2,$3,$4)`, id, t.TransferID, t.CodeSHA256, string(evidence))
	} else if err == nil {
		var old FrozenRedeemReceipt
		if json.Unmarshal(existing, &old) != nil || old != receipt {
			return nil, "", errors.New("source redemption was already transferred with different evidence")
		}
	}
	if err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(RedeemExport{t, receipt})
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(encoded)
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return encoded, hex.EncodeToString(hash[:]), nil
}
