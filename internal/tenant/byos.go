package tenant

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrCredentialNotFound indicates a missing BYOS credential.
var ErrCredentialNotFound = errors.New("tenant: byos credential not found")

// BYOSCredential is a decrypted customer-owned object store credential.
type BYOSCredential struct {
	TenantID     string
	CredentialID string
	Backend      string
	Config       BYOSConfig
	KMSKeyID     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// BYOSConfig contains S3-compatible BYOS connection settings.
type BYOSConfig struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	UseSSL    bool   `json:"use_ssl"`
}

// CredentialCipher encrypts/decrypts tenant credential payloads.
type CredentialCipher interface {
	Encrypt(plaintext, aad []byte) ([]byte, error)
	Decrypt(ciphertext, aad []byte) ([]byte, error)
}

// AESGCMCipher is a local envelope-encryption primitive. Production KMS can wrap the key outside this package.
type AESGCMCipher struct{ aead cipher.AEAD }

// NewAESGCMCipher creates AES-GCM cipher from a 16/24/32-byte key.
func NewAESGCMCipher(key []byte) (*AESGCMCipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMCipher{aead: aead}, nil
}

func (c *AESGCMCipher) Encrypt(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{}, nonce...)
	out = c.aead.Seal(out, nonce, plaintext, aad)
	return out, nil
}

func (c *AESGCMCipher) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < c.aead.NonceSize() {
		return nil, errors.New("tenant: ciphertext too short")
	}
	nonce := ciphertext[:c.aead.NonceSize()]
	sealed := ciphertext[c.aead.NonceSize():]
	return c.aead.Open(nil, nonce, sealed, aad)
}

// SetBYOSCredential encrypts and stores BYOS credential config.
func (s *PGStore) SetBYOSCredential(ctx context.Context, tenantID, credentialID, backend string, cfg BYOSConfig, kmsKeyID string, cipher CredentialCipher) error {
	credentialID = strings.TrimSpace(credentialID)
	backend = strings.TrimSpace(backend)
	if credentialID == "" || backend == "" {
		return errors.New("tenant: credential_id and backend are required")
	}
	if cipher == nil {
		return errors.New("tenant: credential cipher is required")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	enc, err := cipher.Encrypt(raw, byosAAD(tenantID, credentialID, backend))
	if err != nil {
		return err
	}
	return Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO byos_credentials(tenant_id, credential_id, backend, encrypted_config, kms_key_id, updated_at)
			VALUES ($1,$2,$3,$4,$5,now())
			ON CONFLICT (tenant_id, credential_id) DO UPDATE SET
			  backend=EXCLUDED.backend,
			  encrypted_config=EXCLUDED.encrypted_config,
			  kms_key_id=EXCLUDED.kms_key_id,
			  updated_at=now()`, tenantID, credentialID, backend, enc, strings.TrimSpace(kmsKeyID))
		return err
	})
}

// GetBYOSCredential decrypts a stored BYOS credential.
func (s *PGStore) GetBYOSCredential(ctx context.Context, tenantID, credentialID string, cipher CredentialCipher) (*BYOSCredential, error) {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return nil, ErrCredentialNotFound
	}
	if cipher == nil {
		return nil, errors.New("tenant: credential cipher is required")
	}
	var out BYOSCredential
	var enc []byte
	err := Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT tenant_id::text, credential_id, backend, encrypted_config, kms_key_id, created_at, updated_at
			FROM byos_credentials WHERE tenant_id=$1 AND credential_id=$2`, tenantID, credentialID)
		return row.Scan(&out.TenantID, &out.CredentialID, &out.Backend, &enc, &out.KMSKeyID, &out.CreatedAt, &out.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	raw, err := cipher.Decrypt(enc, byosAAD(out.TenantID, out.CredentialID, out.Backend))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out.Config); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveBYOSCredential deletes a stored BYOS credential.
func (s *PGStore) RemoveBYOSCredential(ctx context.Context, tenantID, credentialID string) error {
	return Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM byos_credentials WHERE tenant_id=$1 AND credential_id=$2`, tenantID, strings.TrimSpace(credentialID))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCredentialNotFound
		}
		return nil
	})
}

func byosAAD(tenantID, credentialID, backend string) []byte {
	return []byte(tenantID + "\x00" + credentialID + "\x00" + backend)
}
