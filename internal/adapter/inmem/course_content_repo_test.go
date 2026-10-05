package inmem

import (
	"context"
	"errors"
	"testing"

	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

const (
	tnt = "11111111-1111-7111-8111-111111111111"
	crs = "019e30db-692f-7d10-8ce0-59669fe9298d"
)

func TestCourseContentRepo_SaveGet(t *testing.T) {
	r := NewCourseContentRepo()
	if _, err := r.Get(context.Background(), tnt, crs); !errors.Is(err, cc.ErrNotFound) {
		t.Fatalf("empty Get: want ErrNotFound, got %v", err)
	}
	c, _ := cc.New(cc.NewParams{CourseID: crs, TenantID: tnt})
	if err := r.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(context.Background(), tnt, crs)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CourseID != crs {
		t.Fatalf("round-trip mismatch")
	}
}

func TestCourseContentRepo_SoftDeletedIsNotFound(t *testing.T) {
	r := NewCourseContentRepo()
	c, _ := cc.New(cc.NewParams{CourseID: crs, TenantID: tnt})
	_ = c.SoftDelete()
	_ = r.Save(context.Background(), c)
	if _, err := r.Get(context.Background(), tnt, crs); !errors.Is(err, cc.ErrNotFound) {
		t.Fatalf("soft-deleted Get: want ErrNotFound, got %v", err)
	}
}

func TestCourseContentRepo_TenantIsolation(t *testing.T) {
	r := NewCourseContentRepo()
	c, _ := cc.New(cc.NewParams{CourseID: crs, TenantID: tnt})
	_ = r.Save(context.Background(), c)
	if _, err := r.Get(context.Background(), "other-tenant", crs); !errors.Is(err, cc.ErrNotFound) {
		t.Fatalf("cross-tenant Get must be ErrNotFound, got %v", err)
	}
}
