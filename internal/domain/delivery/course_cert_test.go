// course_cert_test.go — CertDefinition value object + Course cert wiring (CHO-1795).
package delivery

import (
	"errors"
	"testing"
)

func TestCertType_Valid(t *testing.T) {
	for _, ct := range []CertType{CertTypeCompletion, CertTypeCompetency, CertTypeAccredited, CertTypeMicroCredential} {
		if !ct.Valid() {
			t.Fatalf("%q should be valid", ct)
		}
	}
	if CertType("DIPLOMA").Valid() {
		t.Fatal("unknown cert type should be invalid")
	}
}

func TestCertDefinition_Validate(t *testing.T) {
	// disabled is always valid regardless of other fields
	if err := (CertDefinition{Enabled: false, CertType: "BOGUS", PassingScorePct: 999}).Validate(); err != nil {
		t.Fatalf("disabled def should validate, got %v", err)
	}
	// enabled + valid
	if err := (CertDefinition{Enabled: true, CertType: CertTypeCompetency, PassingScorePct: 70}).Validate(); err != nil {
		t.Fatalf("valid enabled def: %v", err)
	}
	// enabled + bad type
	if err := (CertDefinition{Enabled: true, CertType: "BOGUS"}).Validate(); !errors.Is(err, ErrCertTypeInvalid) {
		t.Fatalf("bad type: want ErrCertTypeInvalid, got %v", err)
	}
	// enabled + score out of range (both ends)
	if err := (CertDefinition{Enabled: true, PassingScorePct: 101}).Validate(); !errors.Is(err, ErrCertPassingScoreRange) {
		t.Fatalf("score 101: want ErrCertPassingScoreRange, got %v", err)
	}
	if err := (CertDefinition{Enabled: true, PassingScorePct: -1}).Validate(); !errors.Is(err, ErrCertPassingScoreRange) {
		t.Fatalf("score -1: want ErrCertPassingScoreRange, got %v", err)
	}
	// enabled, no cert_type (allowed — type optional)
	if err := (CertDefinition{Enabled: true, PassingScorePct: 50}).Validate(); err != nil {
		t.Fatalf("enabled no-type: %v", err)
	}
}

func TestCertDefinition_Meets(t *testing.T) {
	d := CertDefinition{Enabled: true, PassingScorePct: 70}
	if !d.Meets(70) || !d.Meets(85) {
		t.Fatal("score >= threshold should meet")
	}
	if d.Meets(69) {
		t.Fatal("score < threshold should not meet")
	}
	if (CertDefinition{Enabled: false, PassingScorePct: 0}).Meets(100) {
		t.Fatal("disabled def is never met")
	}
}

func TestNewCJ2Course_WithCertification(t *testing.T) {
	cert := &CertDefinition{Enabled: true, CertType: CertTypeCompetency, PassingScorePct: 80, RequireAllContent: true}
	c, err := NewCJ2Course(NewCJ2CourseInput{
		TenantID: "019e2f93-d586-71b5-8c3d-e2b0d0d50100", AuthorGCID: "019e2f93-d586-71b5-8c3d-e2b0d0d50101",
		Title: "Cert course", TestSetIDs: []string{"019e2f93-d586-71b5-8c3d-e2b0d0d50208"},
		Certification: cert,
	})
	if err != nil {
		t.Fatalf("NewCJ2Course: %v", err)
	}
	if !c.Certification.Enabled || c.Certification.CertType != CertTypeCompetency || c.Certification.PassingScorePct != 80 {
		t.Fatalf("cert not carried: %+v", c.Certification)
	}
}

func TestNewCJ2Course_RejectsInvalidCert(t *testing.T) {
	_, err := NewCJ2Course(NewCJ2CourseInput{
		TenantID: "019e2f93-d586-71b5-8c3d-e2b0d0d50100", AuthorGCID: "019e2f93-d586-71b5-8c3d-e2b0d0d50101",
		Title: "x", TestSetIDs: []string{"019e2f93-d586-71b5-8c3d-e2b0d0d50208"},
		Certification: &CertDefinition{Enabled: true, PassingScorePct: 200},
	})
	if !errors.Is(err, ErrCertPassingScoreRange) {
		t.Fatalf("want ErrCertPassingScoreRange, got %v", err)
	}
}

func TestUpdateDraftContent_SetsCertification(t *testing.T) {
	c, err := NewCJ2Course(NewCJ2CourseInput{
		TenantID: "019e2f93-d586-71b5-8c3d-e2b0d0d50100", AuthorGCID: "019e2f93-d586-71b5-8c3d-e2b0d0d50101",
		Title: "x", TestSetIDs: []string{"019e2f93-d586-71b5-8c3d-e2b0d0d50208"},
	})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	newCert := &CertDefinition{Enabled: true, CertType: CertTypeCompletion, PassingScorePct: 60, RequireAllContent: false}
	if err := c.UpdateDraftContent(UpdateCourseInput{Certification: newCert}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !c.Certification.Enabled || c.Certification.CertType != CertTypeCompletion || c.Certification.RequireAllContent {
		t.Fatalf("cert not updated: %+v", c.Certification)
	}
	// invalid cert update rejected, state preserved
	if err := c.UpdateDraftContent(UpdateCourseInput{Certification: &CertDefinition{Enabled: true, CertType: "BOGUS"}}); !errors.Is(err, ErrCertTypeInvalid) {
		t.Fatalf("bad cert update: want ErrCertTypeInvalid, got %v", err)
	}
}
