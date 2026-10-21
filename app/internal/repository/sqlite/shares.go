package sqlite

import (
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type Shares struct{ app core.App }

func NewShares(app core.App) *Shares { return &Shares{app: app} }

func (r *Shares) Create(value model.ShareRecord) (*model.ShareRecord, error) {
	collection, err := r.app.FindCollectionByNameOrId(model.SharesCollection)
	if err != nil {
		return nil, err
	}
	record := core.NewRecord(collection)
	record.Set("owner", value.OwnerID)
	record.Set("token_hash", value.TokenHash)
	record.Set("token_cipher", value.TokenCipher)
	record.Set("storage_key", value.StorageKey)
	record.Set("filename", value.Filename)
	record.Set("mime_type", value.MIMEType)
	record.Set("size", value.Size)
	record.Set("expires_at", value.ExpiresAt.Format(types.DefaultDateLayout))
	record.Set("password_hash", value.PasswordHash)
	record.Set("direct", value.Direct)
	if err := r.app.Save(record); err != nil {
		return nil, err
	}
	return shareRecord(record), nil
}

func (r *Shares) ListOwned(userID string) ([]model.ShareRecord, error) {
	records, err := r.app.FindRecordsByFilter(model.SharesCollection, "owner = {:owner}", "-created", 200, 0, dbx.Params{"owner": userID})
	if err != nil {
		return nil, err
	}
	result := make([]model.ShareRecord, 0, len(records))
	for _, record := range records {
		result = append(result, *shareRecord(record))
	}
	return result, nil
}
func (r *Shares) GetOwned(userID, id string) (*model.ShareRecord, error) {
	record, err := r.app.FindFirstRecordByFilter(model.SharesCollection, "id = {:id} && owner = {:owner}", dbx.Params{"id": id, "owner": userID})
	if err != nil {
		return nil, err
	}
	return shareRecord(record), nil
}
func (r *Shares) GetByTokenHash(hash string) (*model.ShareRecord, error) {
	record, err := r.app.FindFirstRecordByFilter(model.SharesCollection, "token_hash = {:hash}", dbx.Params{"hash": hash})
	if err != nil {
		return nil, err
	}
	return shareRecord(record), nil
}
func (r *Shares) Update(id string, expiresAt time.Time, passwordHash *string, direct *bool) (*model.ShareRecord, error) {
	record, err := r.app.FindRecordById(model.SharesCollection, id)
	if err != nil {
		return nil, err
	}
	record.Set("expires_at", expiresAt.Format(types.DefaultDateLayout))
	if passwordHash != nil {
		record.Set("password_hash", *passwordHash)
	}
	if direct != nil {
		record.Set("direct", *direct)
	}
	if err := r.app.Save(record); err != nil {
		return nil, err
	}
	return shareRecord(record), nil
}
func (r *Shares) SetRevoked(id string, revoked bool) error {
	record, err := r.app.FindRecordById(model.SharesCollection, id)
	if err != nil {
		return err
	}
	record.Set("revoked", revoked)
	return r.app.Save(record)
}
func (r *Shares) Delete(id string) error {
	record, err := r.app.FindRecordById(model.SharesCollection, id)
	if err != nil {
		return err
	}
	return r.app.Delete(record)
}
func (r *Shares) AddDownload(id string) error {
	_, err := r.app.DB().NewQuery("UPDATE shares SET downloads = downloads + 1 WHERE id = {:id}").Bind(dbx.Params{"id": id}).Execute()
	return err
}
func (r *Shares) StorageUsed(userID string) (int64, error) {
	var row struct {
		Total int64 `db:"total"`
	}
	err := r.app.DB().NewQuery("SELECT COALESCE(SUM(size), 0) AS total FROM shares WHERE owner = {:owner}").Bind(dbx.Params{"owner": userID}).One(&row)
	return row.Total, err
}

// TotalStorageUsed is the size of every stored share, across all users.
func (r *Shares) TotalStorageUsed() (int64, error) {
	var row struct {
		Total int64 `db:"total"`
	}
	err := r.app.DB().NewQuery("SELECT COALESCE(SUM(size), 0) AS total FROM shares").One(&row)
	return row.Total, err
}
func (r *Shares) Expired(before time.Time, limit int) ([]model.ShareRecord, error) {
	records, err := r.app.FindRecordsByFilter(model.SharesCollection, "expires_at <= {:before}", "expires_at", limit, 0, dbx.Params{"before": before.Format(types.DefaultDateLayout)})
	if err != nil {
		return nil, err
	}
	result := make([]model.ShareRecord, 0, len(records))
	for _, record := range records {
		result = append(result, *shareRecord(record))
	}
	return result, nil
}

func shareRecord(record *core.Record) *model.ShareRecord {
	return &model.ShareRecord{ID: record.Id, OwnerID: record.GetString("owner"), TokenHash: record.GetString("token_hash"), TokenCipher: record.GetString("token_cipher"), StorageKey: record.GetString("storage_key"), Filename: record.GetString("filename"), MIMEType: record.GetString("mime_type"), Size: int64(record.GetFloat("size")), ExpiresAt: record.GetDateTime("expires_at").Time(), Revoked: record.GetBool("revoked"), PasswordHash: record.GetString("password_hash"), Direct: record.GetBool("direct"), Downloads: int64(record.GetFloat("downloads")), Created: record.GetDateTime("created").Time()}
}
