package sqlite

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type Billing struct {
	app core.App
}

func NewBilling(app core.App) *Billing {
	return &Billing{app: app}
}

func (r *Billing) UpsertSubscription(subscription model.BillingSubscriptionRecord) error {
	collection, err := r.app.FindCollectionByNameOrId(model.BillingSubscriptionsCollection)
	if err != nil {
		return err
	}

	record, err := r.app.FindFirstRecordByFilter(
		model.BillingSubscriptionsCollection,
		"external_id = {:external}",
		dbx.Params{"external": subscription.ExternalID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		record = core.NewRecord(collection)
	} else if err != nil {
		return err
	} else if providerRecordIsNewer(record, subscription.ProviderUpdatedAt) {
		return nil
	}

	setSubscriptionFields(record, subscription)
	if err := r.app.Save(record); err == nil {
		return nil
	} else if !isUniqueConstraintError(err) || record.Id != "" {
		return err
	}

	// A concurrent webhook may have inserted the same external subscription.
	record, err = r.app.FindFirstRecordByFilter(
		model.BillingSubscriptionsCollection,
		"external_id = {:external}",
		dbx.Params{"external": subscription.ExternalID},
	)
	if err != nil {
		return err
	}
	if providerRecordIsNewer(record, subscription.ProviderUpdatedAt) {
		return nil
	}
	setSubscriptionFields(record, subscription)
	return r.app.Save(record)
}

func (r *Billing) ListSubscriptions(userID string) ([]model.BillingSubscriptionRecord, error) {
	records, err := r.app.FindRecordsByFilter(
		model.BillingSubscriptionsCollection,
		"user = {:user}",
		"-provider_updated_at",
		100,
		0,
		dbx.Params{"user": userID},
	)
	if err != nil {
		return nil, err
	}

	items := make([]model.BillingSubscriptionRecord, 0, len(records))
	for _, record := range records {
		items = append(items, subscriptionFromRecord(record))
	}
	return items, nil
}

func (r *Billing) FindSubscriptionByExternalID(externalID string) (*model.BillingSubscriptionRecord, error) {
	record, err := r.app.FindFirstRecordByFilter(
		model.BillingSubscriptionsCollection,
		"external_id = {:external}",
		dbx.Params{"external": externalID},
	)
	if err != nil {
		return nil, err
	}
	item := subscriptionFromRecord(record)
	return &item, nil
}

func (r *Billing) UpsertTransaction(transaction model.BillingTransactionRecord) error {
	collection, err := r.app.FindCollectionByNameOrId(model.BillingTransactionsCollection)
	if err != nil {
		return err
	}

	params := dbx.Params{"external": transaction.ExternalID, "type": transaction.ExternalType}
	record, err := r.app.FindFirstRecordByFilter(
		model.BillingTransactionsCollection,
		"external_id = {:external} && external_type = {:type}",
		params,
	)
	if errors.Is(err, sql.ErrNoRows) {
		record = core.NewRecord(collection)
	} else if err != nil {
		return err
	} else if providerRecordIsNewer(record, transaction.ProviderUpdatedAt) {
		return nil
	}

	setTransactionFields(record, transaction)
	if err := r.app.Save(record); err == nil {
		return nil
	} else if !isUniqueConstraintError(err) || record.Id != "" {
		return err
	}

	record, err = r.app.FindFirstRecordByFilter(
		model.BillingTransactionsCollection,
		"external_id = {:external} && external_type = {:type}",
		params,
	)
	if err != nil {
		return err
	}
	if providerRecordIsNewer(record, transaction.ProviderUpdatedAt) {
		return nil
	}
	setTransactionFields(record, transaction)
	return r.app.Save(record)
}

func (r *Billing) ListTransactions(userID string, limit int) ([]model.BillingTransactionRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	records, err := r.app.FindRecordsByFilter(
		model.BillingTransactionsCollection,
		"user = {:user}",
		"-charged_at",
		limit,
		0,
		dbx.Params{"user": userID},
	)
	if err != nil {
		return nil, err
	}

	items := make([]model.BillingTransactionRecord, 0, len(records))
	for _, record := range records {
		items = append(items, transactionFromRecord(record))
	}
	return items, nil
}

func (r *Billing) UserExists(userID string) (bool, error) {
	if strings.TrimSpace(userID) == "" {
		return false, nil
	}
	_, err := r.app.FindRecordById(model.UsersCollection, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *Billing) FindUserIDByEmail(email string) (string, error) {
	record, err := r.app.FindFirstRecordByFilter(
		model.UsersCollection,
		"email = {:email}",
		dbx.Params{"email": strings.ToLower(strings.TrimSpace(email))},
	)
	if err != nil {
		return "", err
	}
	return record.Id, nil
}

func setSubscriptionFields(record *core.Record, subscription model.BillingSubscriptionRecord) {
	record.Set("user", subscription.UserID)
	record.Set("external_id", subscription.ExternalID)
	record.Set("customer_id", subscription.CustomerID)
	record.Set("order_id", subscription.OrderID)
	record.Set("product_id", subscription.ProductID)
	record.Set("variant_id", subscription.VariantID)
	record.Set("plan_key", subscription.PlanKey)
	record.Set("product_name", subscription.ProductName)
	record.Set("variant_name", subscription.VariantName)
	record.Set("status", subscription.Status)
	setOptionalDate(record, "renews_at", subscription.RenewsAt)
	setOptionalDate(record, "ends_at", subscription.EndsAt)
	setOptionalDate(record, "trial_ends_at", subscription.TrialEndsAt)
	setOptionalDate(record, "provider_updated_at", subscription.ProviderUpdatedAt)
	record.Set("test_mode", subscription.TestMode)
}

func setTransactionFields(record *core.Record, transaction model.BillingTransactionRecord) {
	record.Set("user", transaction.UserID)
	record.Set("external_id", transaction.ExternalID)
	record.Set("external_type", transaction.ExternalType)
	record.Set("external_subscription_id", transaction.ExternalSubscriptionID)
	record.Set("billing_reason", transaction.BillingReason)
	record.Set("description", transaction.Description)
	record.Set("amount_cents", transaction.AmountCents)
	record.Set("refunded_amount_cents", transaction.RefundedAmountCents)
	record.Set("currency", strings.ToUpper(transaction.Currency))
	record.Set("status", transaction.Status)
	setOptionalDate(record, "charged_at", transaction.ChargedAt)
	record.Set("invoice_url", transaction.InvoiceURL)
	setOptionalDate(record, "provider_updated_at", transaction.ProviderUpdatedAt)
	record.Set("test_mode", transaction.TestMode)
}

func subscriptionFromRecord(record *core.Record) model.BillingSubscriptionRecord {
	return model.BillingSubscriptionRecord{
		ID:                record.Id,
		UserID:            record.GetString("user"),
		ExternalID:        record.GetString("external_id"),
		CustomerID:        record.GetString("customer_id"),
		OrderID:           record.GetString("order_id"),
		ProductID:         record.GetString("product_id"),
		VariantID:         record.GetString("variant_id"),
		PlanKey:           record.GetString("plan_key"),
		ProductName:       record.GetString("product_name"),
		VariantName:       record.GetString("variant_name"),
		Status:            record.GetString("status"),
		RenewsAt:          record.GetDateTime("renews_at").Time(),
		EndsAt:            record.GetDateTime("ends_at").Time(),
		TrialEndsAt:       record.GetDateTime("trial_ends_at").Time(),
		ProviderUpdatedAt: record.GetDateTime("provider_updated_at").Time(),
		TestMode:          record.GetBool("test_mode"),
	}
}

func transactionFromRecord(record *core.Record) model.BillingTransactionRecord {
	return model.BillingTransactionRecord{
		ID:                     record.Id,
		UserID:                 record.GetString("user"),
		ExternalID:             record.GetString("external_id"),
		ExternalType:           record.GetString("external_type"),
		ExternalSubscriptionID: record.GetString("external_subscription_id"),
		BillingReason:          record.GetString("billing_reason"),
		Description:            record.GetString("description"),
		AmountCents:            int64(record.GetFloat("amount_cents")),
		RefundedAmountCents:    int64(record.GetFloat("refunded_amount_cents")),
		Currency:               record.GetString("currency"),
		Status:                 record.GetString("status"),
		ChargedAt:              record.GetDateTime("charged_at").Time(),
		InvoiceURL:             record.GetString("invoice_url"),
		ProviderUpdatedAt:      record.GetDateTime("provider_updated_at").Time(),
		TestMode:               record.GetBool("test_mode"),
	}
}

func providerRecordIsNewer(record *core.Record, incoming time.Time) bool {
	current := record.GetDateTime("provider_updated_at").Time()
	return !incoming.IsZero() && current.After(incoming)
}

func setOptionalDate(record *core.Record, field string, value time.Time) {
	if value.IsZero() {
		record.Set(field, "")
		return
	}
	record.Set(field, value.UTC())
}

func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
