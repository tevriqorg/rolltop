package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ProviderSyncGate struct {
	UserID                 int64
	Provider               string
	SourceID               string
	CensusMessageCount     int
	CensusEstimatedRaw     int64
	CensusExpectedEvidence int64
	CensusRecommendedFree  int64
	CensusMethod           string
	CensusExact            bool
	CensusAt               time.Time
	BackfillApprovedAt     time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (g ProviderSyncGate) HasCensus() bool        { return !g.CensusAt.IsZero() }
func (g ProviderSyncGate) BackfillApproved() bool { return !g.BackfillApprovedAt.IsZero() }

func normalizeProviderSource(providerName, sourceID string) (string, string, error) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	sourceID = strings.TrimSpace(sourceID)
	if providerName == "" || sourceID == "" {
		return "", "", errors.New("provider gate requires provider and source")
	}
	return providerName, sourceID, nil
}

func (s *Store) SaveProviderCensus(ctx context.Context, userID int64, providerName, sourceID string, messageCount int, estimatedRaw, expectedEvidence, recommendedFree int64, method string, exact bool) (ProviderSyncGate, error) {
	providerName, sourceID, err := normalizeProviderSource(providerName, sourceID)
	if err != nil || userID <= 0 || messageCount < 0 || estimatedRaw < 0 || expectedEvidence < 0 || recommendedFree < 0 {
		if err != nil {
			return ProviderSyncGate{}, err
		}
		return ProviderSyncGate{}, errors.New("invalid provider census")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderSyncGate{}, err
	}
	now := nowUnix()
	_, err = db.ExecContext(ctx, `INSERT INTO provider_sync_gates(user_id,provider,source_id,census_message_count,census_estimated_raw_bytes,census_expected_evidence_bytes,census_recommended_free_disk,census_method,census_exact,census_at,backfill_approved_at,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,0,?,?)
 ON CONFLICT(user_id,provider,source_id) DO UPDATE SET
 census_message_count=excluded.census_message_count,census_estimated_raw_bytes=excluded.census_estimated_raw_bytes,census_expected_evidence_bytes=excluded.census_expected_evidence_bytes,census_recommended_free_disk=excluded.census_recommended_free_disk,census_method=excluded.census_method,census_exact=excluded.census_exact,census_at=excluded.census_at,backfill_approved_at=0,updated_at=excluded.updated_at`, userID, providerName, sourceID, messageCount, estimatedRaw, expectedEvidence, recommendedFree, strings.TrimSpace(method), boolInt(exact), now, now, now)
	if err != nil {
		return ProviderSyncGate{}, err
	}
	return s.GetProviderSyncGate(ctx, userID, providerName, sourceID)
}

func (s *Store) GetProviderSyncGate(ctx context.Context, userID int64, providerName, sourceID string) (ProviderSyncGate, error) {
	providerName, sourceID, err := normalizeProviderSource(providerName, sourceID)
	if err != nil {
		return ProviderSyncGate{}, err
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderSyncGate{}, err
	}
	var g ProviderSyncGate
	var exact int
	var censusAt, approvedAt, createdAt, updatedAt int64
	err = db.QueryRowContext(ctx, `SELECT user_id,provider,source_id,census_message_count,census_estimated_raw_bytes,census_expected_evidence_bytes,census_recommended_free_disk,census_method,census_exact,census_at,backfill_approved_at,created_at,updated_at FROM provider_sync_gates WHERE user_id=? AND provider=? AND source_id=?`, userID, providerName, sourceID).Scan(&g.UserID, &g.Provider, &g.SourceID, &g.CensusMessageCount, &g.CensusEstimatedRaw, &g.CensusExpectedEvidence, &g.CensusRecommendedFree, &g.CensusMethod, &exact, &censusAt, &approvedAt, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return ProviderSyncGate{}, ErrNotFound
	}
	if err != nil {
		return ProviderSyncGate{}, err
	}
	g.CensusExact = exact != 0
	if censusAt > 0 {
		g.CensusAt = unixTime(censusAt)
	}
	if approvedAt > 0 {
		g.BackfillApprovedAt = unixTime(approvedAt)
	}
	g.CreatedAt = unixTime(createdAt)
	g.UpdatedAt = unixTime(updatedAt)
	return g, nil
}

func (s *Store) ApproveProviderBackfill(ctx context.Context, userID int64, providerName, sourceID string) (ProviderSyncGate, error) {
	providerName, sourceID, err := normalizeProviderSource(providerName, sourceID)
	if err != nil {
		return ProviderSyncGate{}, err
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderSyncGate{}, err
	}
	now := nowUnix()
	res, err := db.ExecContext(ctx, `UPDATE provider_sync_gates SET backfill_approved_at=?,updated_at=? WHERE user_id=? AND provider=? AND source_id=? AND census_at>0`, now, now, userID, providerName, sourceID)
	if err != nil {
		return ProviderSyncGate{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ProviderSyncGate{}, err
	}
	if n == 0 {
		return ProviderSyncGate{}, errors.New("provider backfill requires a completed census")
	}
	return s.GetProviderSyncGate(ctx, userID, providerName, sourceID)
}
