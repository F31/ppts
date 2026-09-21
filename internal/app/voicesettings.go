package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VoiceSettings 是项目级语音属性（语音模型 / 音色 / 语速）。
type VoiceSettings struct {
	Model       string
	Voice       string
	RatePercent int
}

// DefaultVoiceSettings 是未保存过时的缺省值。
func DefaultVoiceSettings() VoiceSettings {
	return VoiceSettings{RatePercent: 100}
}

// VoiceSettingsStore 持久化项目级语音属性。
type VoiceSettingsStore interface {
	Get(ctx context.Context, tenantID, projectID string) (VoiceSettings, error)
	Save(ctx context.Context, tenantID, projectID string, settings VoiceSettings) error
}

type voiceSettingsPGStore struct {
	pool *pgxpool.Pool
}

type voiceSettingsSQLiteStore struct {
	db *sql.DB
}

// NewVoiceSettingsStore 创建基于 pgxpool 的语音属性存储。
func NewVoiceSettingsStore(pool *pgxpool.Pool) VoiceSettingsStore {
	return &voiceSettingsPGStore{pool: pool}
}

// NewSQLiteVoiceSettingsStore 创建 SQLite 单租户部署使用的语音属性存储。
func NewSQLiteVoiceSettingsStore(db *sql.DB) VoiceSettingsStore {
	return &voiceSettingsSQLiteStore{db: db}
}

func normalizeVoiceSettings(in VoiceSettings) VoiceSettings {
	if in.RatePercent < 50 || in.RatePercent > 200 {
		in.RatePercent = 100
	}
	return in
}

func (s *voiceSettingsPGStore) Get(ctx context.Context, tenantID, projectID string) (VoiceSettings, error) {
	if tenantID == "" || projectID == "" {
		return DefaultVoiceSettings(), errors.New("voice_settings: tenant/project id required")
	}
	var out VoiceSettings
	err := s.pool.QueryRow(ctx, `
		SELECT model, voice, rate_percent FROM project_voice_settings
		WHERE tenant_id = $1 AND project_id = $2
	`, tenantID, projectID).Scan(&out.Model, &out.Voice, &out.RatePercent)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultVoiceSettings(), nil
	}
	if err != nil {
		return DefaultVoiceSettings(), err
	}
	return normalizeVoiceSettings(out), nil
}

func (s *voiceSettingsPGStore) Save(ctx context.Context, tenantID, projectID string, settings VoiceSettings) error {
	if tenantID == "" || projectID == "" {
		return errors.New("voice_settings: tenant/project id required")
	}
	settings = normalizeVoiceSettings(settings)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project_voice_settings (tenant_id, project_id, model, voice, rate_percent, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (tenant_id, project_id)
		DO UPDATE SET model = EXCLUDED.model, voice = EXCLUDED.voice,
			rate_percent = EXCLUDED.rate_percent, updated_at = now()
	`, tenantID, projectID, settings.Model, settings.Voice, settings.RatePercent)
	return err
}

func (s *voiceSettingsSQLiteStore) Get(ctx context.Context, tenantID, projectID string) (VoiceSettings, error) {
	if tenantID == "" || projectID == "" {
		return DefaultVoiceSettings(), errors.New("voice_settings: tenant/project id required")
	}
	var out VoiceSettings
	err := s.db.QueryRowContext(ctx, `
		SELECT model, voice, rate_percent FROM project_voice_settings
		WHERE tenant_id = ? AND project_id = ?
	`, tenantID, projectID).Scan(&out.Model, &out.Voice, &out.RatePercent)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultVoiceSettings(), nil
	}
	if err != nil {
		return DefaultVoiceSettings(), err
	}
	return normalizeVoiceSettings(out), nil
}

func (s *voiceSettingsSQLiteStore) Save(ctx context.Context, tenantID, projectID string, settings VoiceSettings) error {
	if tenantID == "" || projectID == "" {
		return errors.New("voice_settings: tenant/project id required")
	}
	settings = normalizeVoiceSettings(settings)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_voice_settings (tenant_id, project_id, model, voice, rate_percent, updated_at)
		VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT (tenant_id, project_id)
		DO UPDATE SET model = excluded.model, voice = excluded.voice,
			rate_percent = excluded.rate_percent,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, tenantID, projectID, settings.Model, settings.Voice, settings.RatePercent)
	return err
}
