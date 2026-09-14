package s3

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

func TestRegionBucketNormalization(t *testing.T) {
	cases := []struct {
		base, region, want string
	}{
		{"ppts", "cn-east-1", "ppts-cn-east-1"},
		{"ppts", "CN_EAST_1", "ppts-cn-east-1"},
		{"ppts", "ap-south-1 ", "ppts-ap-south-1"},
		{"ppts", "", "ppts"},
		{"ppts", "US-West", "ppts-us-west"},
	}
	for _, c := range cases {
		got := regionBucket(c.base, c.region)
		if got != c.want {
			t.Fatalf("regionBucket(%q,%q) = %q want %q", c.base, c.region, got, c.want)
		}
	}
}

type emptyRegionResolver struct{}

func (emptyRegionResolver) StorageRegion(context.Context, string) (string, error) { return "", nil }

func TestRegionRouterDefaultsToBaseWithoutRegion(t *testing.T) {
	if os.Getenv("S3_ENDPOINT") == "" {
		t.Skip("S3 adapter test requires S3_ENDPOINT (e.g. localhost:9000)")
	}
	base := setupS3(t)
	router := NewRegionRouter(base, emptyRegionResolver{})
	k := s3Key()
	body := "region-default"
	if err := router.Put(context.Background(), k, strings.NewReader(body), objectstore.ObjectMeta{ContentType: "text/plain", Size: int64(len(body))}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	st, err := router.storeFor(context.Background(), k.TenantID)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if st != base {
		t.Fatalf("storeFor returned a store different from base")
	}
	if err := router.Delete(context.Background(), k); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
