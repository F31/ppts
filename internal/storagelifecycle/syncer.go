// Package storagelifecycle periodically applies tenant storage lifecycle policy to object backends.
package storagelifecycle

import (
	"context"
	"errors"
	"log"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/tenant"
)

// PolicyLister reads active tenant lifecycle policy rows from the control plane.
type PolicyLister interface {
	ListLifecyclePolicies(ctx context.Context) ([]tenant.LifecyclePolicySetting, error)
}

type tenantLifecycleApplier interface {
	ApplyTenantLifecyclePolicy(ctx context.Context, tenantID, bucket string, policy objectstore.LifecyclePolicy) error
}

// Syncer applies explicit storage lifecycle settings. It intentionally does not translate
// source_retention_days into a bucket expiration rule because that would delete all assets.
type Syncer struct {
	policies PolicyLister
	objects  objectstore.ObjectStore
	logger   *log.Logger
}

// NewSyncer creates a lifecycle syncer.
func NewSyncer(policies PolicyLister, objects objectstore.ObjectStore, logger *log.Logger) *Syncer {
	return &Syncer{policies: policies, objects: objects, logger: logger}
}

// Sync performs one pass over active tenant policies. Unsupported backends are skipped.
func (s *Syncer) Sync(ctx context.Context) error {
	settings, err := s.policies.ListLifecyclePolicies(ctx)
	if err != nil {
		return err
	}
	for _, setting := range settings {
		policy, ok := buildPolicy(setting.Policy)
		if !ok {
			continue
		}
		if err := s.apply(ctx, setting.TenantID, policy); err != nil {
			if errors.Is(err, objectstore.ErrOperationNotSupported) {
				continue
			}
			if s.logger != nil {
				s.logger.Printf("storage lifecycle: tenant %s apply failed: %v", setting.TenantID, err)
			}
		}
	}
	return nil
}

func (s *Syncer) apply(ctx context.Context, tenantID string, policy objectstore.LifecyclePolicy) error {
	if applier, ok := s.objects.(tenantLifecycleApplier); ok {
		return applier.ApplyTenantLifecyclePolicy(ctx, tenantID, "", policy)
	}
	return s.objects.ApplyLifecyclePolicy(ctx, "", policy)
}

func buildPolicy(p tenant.Policy) (objectstore.LifecyclePolicy, bool) {
	var out objectstore.LifecyclePolicy
	if p.StorageTransitionDays > 0 {
		out.Transitions = append(out.Transitions, objectstore.LifecycleTransition{
			AfterDays: p.StorageTransitionDays,
			To:        objectstore.StorageClassInfrequent,
		})
	}
	if p.StorageExpirationDays > 0 {
		out.Expiration = &objectstore.LifecycleExpiration{AfterDays: p.StorageExpirationDays}
	}
	return out, len(out.Transitions) > 0 || out.Expiration != nil
}
