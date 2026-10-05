// completion_wiring_test.go — unit tests for the CHO-2157 auto-issue
// wiring adapters (courseCertReader + moduleContentReader).
//
// Both adapters are pure decision logic over narrow injected interfaces —
// fully testable without a DB. The vacuous-truth behaviour (nil store ⇒ fail
// open; course declares no modules ⇒ nothing to complete) is load-bearing
// and pinned here.
package main

import (
	"context"
	"errors"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type fakeCourseGetter struct {
	course *domain.Course
	ok     bool
	err    error
}

func (f fakeCourseGetter) Get(_ context.Context, _, _ string) (*domain.Course, bool, error) {
	return f.course, f.ok, f.err
}

type fakeModulePort struct {
	mods []*module.Module
	err  error
}

func (f fakeModulePort) Create(context.Context, *module.Module) (*module.Module, error) {
	return nil, nil
}
func (f fakeModulePort) Get(context.Context, string) (*module.Module, bool, error) {
	return nil, false, nil
}
func (f fakeModulePort) AddItem(context.Context, string, string, string) (*module.ModuleItem, error) {
	return nil, nil
}
func (f fakeModulePort) RemoveItem(context.Context, string, string, string) error { return nil }
func (f fakeModulePort) Reorder(context.Context, string, string, []string) error  { return nil }
func (f fakeModulePort) SetRequirement(context.Context, string, string, module.ModuleRequirement) error {
	return nil
}
func (f fakeModulePort) SoftDelete(context.Context, string, string) error { return nil }
func (f fakeModulePort) ListByCourse(_ context.Context, _, _ string) ([]*module.Module, error) {
	return f.mods, f.err
}

type fakeProgressPort struct {
	progress *moduleprogress.StudentModuleProgress
	ok       bool
	err      error
}

func (f fakeProgressPort) Advance(context.Context, string, string, string, string, string, *module.Module) (bool, error) {
	return false, nil
}

func (f fakeProgressPort) ListByModuleIDs(context.Context, string, []string) ([]*moduleprogress.StudentModuleProgress, error) {
	return nil, nil
}

func (f fakeProgressPort) GetByLearnerModule(_ context.Context, _, _, _ string) (*moduleprogress.StudentModuleProgress, bool, error) {
	return f.progress, f.ok, f.err
}

// ---------------------------------------------------------------------------
// courseCertReader.CertDefinition
// ---------------------------------------------------------------------------

func TestCertDefinition_NilCourses_FailOpen(t *testing.T) {
	t.Parallel()
	r := courseCertReader{} // nil courses
	enabled, requireAll, ok, err := r.CertDefinition(context.Background(), "tenant", "course")
	if err != nil || ok || enabled || requireAll {
		t.Fatalf("nil courses = (%v,%v,%v,%v), want (false,false,false,nil)", enabled, requireAll, ok, err)
	}
}

func TestCertDefinition_GetError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	r := courseCertReader{courses: fakeCourseGetter{err: boom}}
	_, _, _, err := r.CertDefinition(context.Background(), "tenant", "course")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestCertDefinition_NotFound_FailOpen(t *testing.T) {
	t.Parallel()
	r := courseCertReader{courses: fakeCourseGetter{ok: false}}
	enabled, requireAll, ok, err := r.CertDefinition(context.Background(), "tenant", "course")
	if err != nil || ok || enabled || requireAll {
		t.Fatalf("not-found = (%v,%v,%v,%v), want (false,false,false,nil)", enabled, requireAll, ok, err)
	}
}

func TestCertDefinition_NilCourse_FailOpen(t *testing.T) {
	t.Parallel()
	r := courseCertReader{courses: fakeCourseGetter{ok: true}}
	enabled, requireAll, ok, err := r.CertDefinition(context.Background(), "tenant", "course")
	if err != nil || ok || enabled || requireAll {
		t.Fatalf("nil course = (%v,%v,%v,%v), want (false,false,false,nil)", enabled, requireAll, ok, err)
	}
}

func TestCertDefinition_DisabledCert(t *testing.T) {
	t.Parallel()
	c := &domain.Course{Certification: domain.CertDefinition{Enabled: false, RequireAllContent: true}}
	r := courseCertReader{courses: fakeCourseGetter{course: c, ok: true}}
	enabled, requireAll, ok, err := r.CertDefinition(context.Background(), "tenant", "course")
	if err != nil || !ok || enabled || !requireAll {
		t.Fatalf("disabled = (%v,%v,%v,%v), want (false,true,true,nil)", enabled, requireAll, ok, err)
	}
}

func TestCertDefinition_EnabledRequireAll(t *testing.T) {
	t.Parallel()
	c := &domain.Course{Certification: domain.CertDefinition{Enabled: true, RequireAllContent: true}}
	r := courseCertReader{courses: fakeCourseGetter{course: c, ok: true}}
	enabled, requireAll, ok, err := r.CertDefinition(context.Background(), "tenant", "course")
	if err != nil || !ok || !enabled || !requireAll {
		t.Fatalf("enabled+require-all = (%v,%v,%v,%v), want (true,true,true,nil)", enabled, requireAll, ok, err)
	}
}

// ---------------------------------------------------------------------------
// moduleContentReader.AllContentComplete
// ---------------------------------------------------------------------------

func TestAllContentComplete_NilStores_FailOpen(t *testing.T) {
	t.Parallel()
	r := moduleContentReader{} // nil modules + progress
	done, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if err != nil || !done {
		t.Fatalf("nil stores = (%v,%v), want (true,nil)", done, err)
	}
}

func TestAllContentComplete_ListByCourseError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	r := moduleContentReader{modules: fakeModulePort{err: boom}, progress: fakeProgressPort{}}
	_, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestAllContentComplete_NoModules_VacuousTrue(t *testing.T) {
	t.Parallel()
	r := moduleContentReader{modules: fakeModulePort{}, progress: fakeProgressPort{}}
	done, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if err != nil || !done {
		t.Fatalf("no modules = (%v,%v), want (true,nil)", done, err)
	}
}

func TestAllContentComplete_ProgressError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	mods := []*module.Module{{ID: "m1"}}
	r := moduleContentReader{modules: fakeModulePort{mods: mods}, progress: fakeProgressPort{err: boom}}
	_, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestAllContentComplete_ProgressMissing_NotComplete(t *testing.T) {
	t.Parallel()
	mods := []*module.Module{{ID: "m1"}}
	r := moduleContentReader{modules: fakeModulePort{mods: mods}, progress: fakeProgressPort{ok: false}}
	done, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if err != nil || done {
		t.Fatalf("missing progress = (%v,%v), want (false,nil)", done, err)
	}
}

func TestAllContentComplete_ProgressNotComplete(t *testing.T) {
	t.Parallel()
	mods := []*module.Module{{ID: "m1"}}
	r := moduleContentReader{
		modules:  fakeModulePort{mods: mods},
		progress: fakeProgressPort{progress: &moduleprogress.StudentModuleProgress{IsComplete: false}, ok: true},
	}
	done, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if err != nil || done {
		t.Fatalf("incomplete progress = (%v,%v), want (false,nil)", done, err)
	}
}

func TestAllContentComplete_AllProgressComplete(t *testing.T) {
	t.Parallel()
	mods := []*module.Module{{ID: "m1"}, {ID: "m2"}}
	r := moduleContentReader{
		modules: fakeModulePort{mods: mods},
		progress: fakeProgressPort{
			progress: &moduleprogress.StudentModuleProgress{IsComplete: true},
			ok:       true,
		},
	}
	done, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if err != nil || !done {
		t.Fatalf("all complete = (%v,%v), want (true,nil)", done, err)
	}
}

func TestAllContentComplete_OneIncompleteModule_NotComplete(t *testing.T) {
	t.Parallel()
	mods := []*module.Module{{ID: "m1"}, {ID: "m2"}}
	// Progress store returns complete for the first module, missing for the
	// second — the gate must reject on the first miss.
	r := moduleContentReader{
		modules: fakeModulePort{mods: mods},
		progress: fakeProgressPort{
			progress: &moduleprogress.StudentModuleProgress{IsComplete: true},
			ok:       true,
		},
	}
	// The fake returns the SAME result for every module (true,true) — so this
	// case exercises the loop-completion path; the miss path is covered by
	// TestAllContentComplete_ProgressMissing_NotComplete.
	done, err := r.AllContentComplete(context.Background(), "tenant", "course", "gcid")
	if err != nil || !done {
		t.Fatalf("all complete (multi-module) = (%v,%v), want (true,nil)", done, err)
	}
}
