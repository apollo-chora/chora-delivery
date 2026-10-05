// role_vocabulary_test.go: RED-first guard for the chora-delivery role-gate
// vocabulary defect class.
//
// THE DEFECT CLASS
// ----------------
// A gate's accept-list and the role vocabulary the rest of the estate emits
// were written by different hands and never compared. The failure mode is a
// 403 that NAMES a role requirement, which is indistinguishable from a
// permissions decision somebody made on purpose: so it survives review.
//
// The two instances this file pins:
//
//  1. TRAINING ADMIN SPELLING. chora-gateway mints the label with an
//     UNDERSCORE (mint_handler.go `TrainingAdminRole = "training_admin"`,
//     appended by expandTrainingAdmin) and forwards the roles claim VERBATIM
//     onto `x-mesh-user-roles` (jwt_auth.go → servicemesh.MarshalToHeaders).
//     chora-delivery's `roleTrainingAdmin` constant was the HYPHEN spelling,
//     which nothing in the estate has ever minted. Nine gates masked it by
//     ALSO listing the underscore literal inline; two did not.
//
//  2. PROCTOR (ADR-191). The role carries five logistics capabilities
//     (role-capabilities.ts: sitting_check_in / sitting_open / sitting_close /
//     roster_view / incident_file) and was admitted by NO chora-delivery
//     handler. Owner ruling: the handlers are wrong, not the ADR.
//
// WHY EVERY CASE BELOW IS A POSITIVE ASSERTION
// --------------------------------------------
// A test that only asserts the refusal arm passes whether the accept-list is
// right or wrong. Worse, chora-delivery's existing exam/applications suites
// plant `"training-admin"`: a string production never emits: so their green
// says nothing about the deployed persona. Every gate here is therefore asked
// POSITIVELY, with the exact header value the gateway actually produces, and
// each over-grant boundary gets its own negative control.
package httpapi_test

import (
	"net/http"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

// -----------------------------------------------------------------------------
// The production wire vocabulary.
// -----------------------------------------------------------------------------

const (
	// meshRolesRPlusPersona is the EXACT x-mesh-user-roles value chora-gateway
	// produces for the R+ delivery persona: expandTrainingAdmin appends the
	// label to an instructor membership and never replaces it, so the two
	// tokens always travel together.
	meshRolesRPlusPersona = "instructor,training_admin"

	// meshRolesTrainingAdminOnly is training_admin standing alone. It is not
	// mintable today (the label is derived from the instructor row), but every
	// gate that names the role MUST honour it: ADR-239 Alternative C keeps a
	// standalone-role split open, and a gate that only works because a
	// SIBLING role happens to co-occur is a dead branch wearing a passing test.
	meshRolesTrainingAdminOnly = "training_admin"

	// meshRolesProctor is the PROCTOR claim as it reaches a downstream gate.
	// Lowercase per the JWT-extension precedent (training_admin,
	// platform_operator are both minted lowercase) and matching the token
	// chora-creation's live embargo already keys on
	// (exam_embargo.go `proctorRoleToken = "proctor"`).
	meshRolesProctor = "proctor"

	// meshRolesLearner is the negative control for every gate below.
	meshRolesLearner = "learner"
)

// -----------------------------------------------------------------------------
// 1. Training-admin spelling: the two gates that carried ONLY the dead arm.
// -----------------------------------------------------------------------------

// The applications admin queue is the LIVE 403: authorisedForApplicationsAdmin
// accepts `training-admin` (never minted) or `admin`, and does NOT accept
// `instructor`. So the R+ persona: which carries instructor+training_admin and
// no `admin`: is refused outright.
func TestApplicationsAdminGate_AdmitsMintedRPlusPersona(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, adminGCID, meshRolesRPlusPersona)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200: the gateway mints %q for the R+ persona; body=%s",
			w.Code, meshRolesRPlusPersona, w.Body.String())
	}
}

func TestApplicationsAdminGate_AdmitsTrainingAdminAlone(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, adminGCID, meshRolesTrainingAdminOnly)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 for %q; body=%s",
			w.Code, meshRolesTrainingAdminOnly, w.Body.String())
	}
}

// Negative control: the widening must not open the queue to a learner.
func TestApplicationsAdminGate_RefusesLearner(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, adminGCID, meshRolesLearner)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 for %q; body=%s", w.Code, meshRolesLearner, w.Body.String())
	}
}

// hasInstructorRole gates 19 call sites. Its training-admin arm is dead; the
// gate only admits the R+ persona because `instructor` rides alongside. Ask it
// positively with the label standing alone.
func TestAssessmentGate_AdmitsTrainingAdminAlone(t *testing.T) {
	srv, _, _, _, _ := newAssessmentTestServer(t)
	w := reqAdminGET(t, srv, "/api/v1/assessments", adminTenantID, adminGCID, meshRolesTrainingAdminOnly)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 for %q; body=%s",
			w.Code, meshRolesTrainingAdminOnly, w.Body.String())
	}
}

func TestAssessmentGate_RefusesLearner(t *testing.T) {
	srv, _, _, _, _ := newAssessmentTestServer(t)
	w := reqAdminGET(t, srv, "/api/v1/assessments", adminTenantID, adminGCID, meshRolesLearner)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 for %q; body=%s", w.Code, meshRolesLearner, w.Body.String())
	}
}

// Control on a sibling gate that ALREADY listed the underscore inline. It must
// stay green: the normalisation must widen the two broken gates without
// changing what the nine already-correct ones accept.
func TestExamAdminGate_StillAdmitsMintedRPlusPersona(t *testing.T) {
	h, _ := sitServer()
	body := `{"room_id":"019e2f93-d586-71b5-8c3d-e2b0d0d5f400",` + validWindow + `,"capacity":30}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body,
		stTenantID, stAdminGCID, meshRolesRPlusPersona)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 for %q; body=%s", rec.Code, meshRolesRPlusPersona, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 2. PROCTOR: the four capability-bearing writes it must be admitted to.
// -----------------------------------------------------------------------------

// proctorSitting returns a server plus a SCHEDULED sitting created by an admin.
func proctorSitting(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, _ := sitServer()
	return h, createSitting(t, h)
}

func sittingActionPath(sittingID, action string) string {
	return "/api/v1/exams/exam-x/sittings/" + sittingID + "/" + action
}

// exam:sitting_open
func TestProctor_SittingOpen_Admitted(t *testing.T) {
	h, sittingID := proctorSitting(t)
	rec := doSit(t, h, http.MethodPost, sittingActionPath(sittingID, "open"), "",
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusOK {
		t.Fatalf("open: status=%d want 200 (ADR-191 exam:sitting_open); body=%s", rec.Code, rec.Body.String())
	}
}

// exam:sitting_close
func TestProctor_SittingClose_Admitted(t *testing.T) {
	h, sittingID := proctorSitting(t)
	// Admin opens (SCHEDULED → OPEN) so the close is a legal FSM move.
	if rec := doSit(t, h, http.MethodPost, sittingActionPath(sittingID, "open"), "",
		stTenantID, stAdminGCID, stAdminRole); rec.Code != http.StatusOK {
		t.Fatalf("seed open: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := doSit(t, h, http.MethodPost, sittingActionPath(sittingID, "close"), "",
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusOK {
		t.Fatalf("close: status=%d want 200 (ADR-191 exam:sitting_close); body=%s", rec.Code, rec.Body.String())
	}
}

// exam:incident_file
func TestProctor_IncidentFile_Admitted(t *testing.T) {
	h, sittingID := proctorSitting(t)
	body := `{"kind":"device_violation","narrative":"smartwatch detected on wrist"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/incidents", body,
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusCreated {
		t.Fatalf("incident: status=%d want 201 (ADR-191 exam:incident_file); body=%s", rec.Code, rec.Body.String())
	}
}

// exam:sitting_check_in: the check-in pair (verify then admit).
func TestProctor_CandidateVerify_Admitted(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: status=%d want 200 (ADR-191 exam:sitting_check_in); body=%s", rec.Code, rec.Body.String())
	}
}

func TestProctor_CandidateAdmit_Admitted(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	if rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, stAdminRole); rec.Code != http.StatusOK {
		t.Fatalf("seed verify: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := doCand(t, srv, "POST", admitPath(), "", candTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusOK {
		t.Fatalf("admit: status=%d want 200 (ADR-191 exam:sitting_check_in); body=%s", rec.Code, rec.Body.String())
	}
}

// exam:roster_view: PRE-EXISTING behaviour. The candidate / invigilator list
// reads are gated on tenant + gcid only, so this capability is already
// satisfiable. It is asserted here so the report can distinguish "already
// worked" from "this change fixed it", and so a future tightening of the read
// paths cannot silently strip a ratified PROCTOR capability.
func TestProctor_RosterView_AlreadyAdmitted(t *testing.T) {
	h, sittingID := proctorSitting(t)
	rec := doSit(t, h, http.MethodGet, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", "",
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusOK {
		t.Fatalf("roster: status=%d want 200 (ADR-191 exam:roster_view); body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 3. PROCTOR over-grant boundary: the negative controls.
//
// hasExamAdminRole guards 13 call sites; only four of them carry a ratified
// PROCTOR capability. Widening that one gate would hand a proctor sitting
// creation, candidate allocation, invigilator assignment and exam-FORM
// authoring: the last of which is the very content ADR-191 embargoes. Each
// boundary gets its own assertion so the admission stays per-operation.
// -----------------------------------------------------------------------------

// exam:sitting_begin. The SIXTH capability: the owner amended ADR-191 on
// 2026-09-06 to add `begin` (OPEN to IN_PROGRESS) after this gate shipped
// admitting only five. The argument that carried it is that a proctor is the
// person in the room, and starting the sitting is what that person does.
func TestProctor_SittingBegin_Admitted(t *testing.T) {
	h, sittingID := proctorSitting(t)
	if rec := doSit(t, h, http.MethodPost, sittingActionPath(sittingID, "open"), "",
		stTenantID, stAdminGCID, stAdminRole); rec.Code != http.StatusOK {
		t.Fatalf("seed open: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := doSit(t, h, http.MethodPost, sittingActionPath(sittingID, "begin"), "",
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: status=%d want 200 (ADR-191 as amended, exam:sitting_begin); body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestProctor_SittingCancel_Refused(t *testing.T) {
	h, sittingID := proctorSitting(t)
	rec := doSit(t, h, http.MethodPost, sittingActionPath(sittingID, "cancel"), "",
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cancel: status=%d want 403: the amendment added begin, NOT cancel, which sitting.go documents as an admin action; body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestProctor_SittingCreate_Refused(t *testing.T) {
	h, _ := sitServer()
	body := `{"room_id":"019e2f93-d586-71b5-8c3d-e2b0d0d5f400",` + validWindow + `,"capacity":30}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body,
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create: status=%d want 403: scheduling a sitting is not a PROCTOR capability; body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestProctor_CandidateAllocate_Refused(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, "POST", allocPath(), allocBody(), candTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("allocate: status=%d want 403: allocating candidates is not a PROCTOR capability; body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestProctor_InvigilatorAssign_Refused(t *testing.T) {
	h, sittingID := proctorSitting(t)
	body := `{"invigilator_gcid":"` + stInvig2 + `","rank":"invigilator"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body,
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("assign: status=%d want 403: a proctor does not staff the sitting; body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestProctor_InvigilatorUnassign_Refused(t *testing.T) {
	h, sittingID := proctorSitting(t)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body,
		stTenantID, stAdminGCID, stAdminRole)
	ivID := decodeSitMap(t, rec)["id"].(string)
	rec = doSit(t, h, http.MethodDelete, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators/"+ivID, "",
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unassign: status=%d want 403: a proctor does not staff the sitting; body=%s",
			rec.Code, rec.Body.String())
	}
}

// The content boundary. ADR-191 D2 embargoes PROCTOR from exam content; an
// exam FORM is the assembled item set. This must stay 403 no matter what the
// sitting-operations gate admits.
func TestProctor_ExamFormCreate_Refused(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(),
		examTestTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("form create: status=%d want 403: ADR-191 D2 embargoes PROCTOR from exam content; body=%s",
			rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 4. The vocabulary itself.
//
// The defect was a name, so this asserts the name. Both spellings of every
// role that has ever had two must resolve to the same gate answer; a future
// hand that adds a hyphenated or upper-cased token cannot reintroduce the
// split without turning this red.
// -----------------------------------------------------------------------------

func TestMeshRoleVocabulary_SpellingIsIrrelevant(t *testing.T) {
	spellings := []string{
		"training_admin",    // as minted by chora-gateway
		"training-admin",    // the legacy hyphen chora-delivery used to require
		"TRAINING_ADMIN",    // the ADR-141 canonical uppercase form
		"  training_admin ", // whitespace around a comma-separated token
	}
	for _, roles := range spellings {
		srv, _ := seedAdminQueueApps(t)
		w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, adminGCID, roles)
		if w.Code != http.StatusOK {
			t.Errorf("roles=%q: status=%d want 200: every spelling of one role must gate alike; body=%s",
				roles, w.Code, w.Body.String())
		}
	}
}

func TestMeshRoleVocabulary_ProctorSpellingIsIrrelevant(t *testing.T) {
	for _, roles := range []string{"proctor", "PROCTOR", "instructor,proctor"} {
		h, _ := sitServer()
		sittingID := createSitting(t, h)
		rec := doSit(t, h, http.MethodPost, sittingActionPath(sittingID, "open"), "",
			stTenantID, stInvigGCID, roles)
		if rec.Code != http.StatusOK {
			t.Errorf("roles=%q: status=%d want 200; body=%s", roles, rec.Code, rec.Body.String())
		}
	}
}

// Guards the wiring the whole file depends on: an unwired incidents store must
// still 503 rather than silently accept a proctor write.
func TestProctor_IncidentFile_UnwiredStoreStill503(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterExamSittingRoutes(mux, &httpapi.ExamSittingDeps{
		Sittings:     inmem.NewSittingRepo(),
		Invigilators: inmem.NewInvigilatorRepo(),
		Incidents:    nil,
	})
	body := `{"kind":"other","narrative":"x"}`
	rec := doSit(t, mux, http.MethodPost, "/api/v1/exams/exam-x/sittings/some-id/incidents", body,
		stTenantID, stInvigGCID, meshRolesProctor)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 for an unwired incidents store; body=%s", rec.Code, rec.Body.String())
	}
}
