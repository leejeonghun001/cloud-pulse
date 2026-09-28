package cloud

import (
	"context"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// bucketCollector is a local copy of the hub.BucketCollector interface
// (internal/hub is being written concurrently and cannot be imported
// here without violating the Clean Architecture dependency direction
// anyway: internal/cloud is an adapter and must not depend on
// internal/hub, which defines this interface as a consumer).
// Signature per SPEC: Name() string; Collect(ctx) ([]models.BucketStats, error).
type bucketCollector interface {
	Name() string
	Collect(ctx context.Context) ([]models.BucketStats, error)
}

// Compile-time assertions that both collectors satisfy the
// BucketCollector contract expected by internal/hub.
var (
	_ bucketCollector = (*S3Collector)(nil)
	_ bucketCollector = (*R2Collector)(nil)
)
