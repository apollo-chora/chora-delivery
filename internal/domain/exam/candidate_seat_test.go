// candidate_seat_test.go — TDD RED for Candidate.OccupiesSeat (CHO-2105).
//
// The exam candidate-count projection counts only seat-occupying candidates so
// the Overview capacity figure reflects real allocation (distinct from the
// self-enrolment EnrolledCount). REJECTED / WITHDRAWN release the seat.
package exam

import "testing"

func TestCandidate_OccupiesSeat(t *testing.T) {
	cases := []struct {
		state CandidateState
		want  bool
	}{
		{CandidateStateAllocated, true},
		{CandidateStateIDVerified, true},
		{CandidateStateAdmitted, true},
		{CandidateStateRejected, false},
		{CandidateStateWithdrawn, false},
	}
	for _, tc := range cases {
		c := &Candidate{State: tc.state}
		if got := c.OccupiesSeat(); got != tc.want {
			t.Errorf("OccupiesSeat(%s) = %v, want %v", tc.state, got, tc.want)
		}
	}
}
