package clients

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"determined/src/models"
)

// fakeStallSink records the last stall choice relayed to it and reports a
// configurable "pending wait" answer, standing in for PlanStatusService.
type fakeStallSink struct {
	pending  bool
	decision models.StallDecision
	comment  string
	calls    int
}

func (f *fakeStallSink) SubmitStallChoice(decision models.StallDecision, comment string) bool {
	f.calls++
	f.decision = decision
	f.comment = comment
	return f.pending
}

type stubSource struct{}

func (stubSource) Snapshot() models.PlanSessionStatus { return models.PlanSessionStatus{} }
func (stubSource) Subscribe() (<-chan models.PlanSessionStatus, func()) {
	ch := make(chan models.PlanSessionStatus)
	return ch, func() {}
}

type stubClock struct{}

func (stubClock) Now() time.Time { return time.Unix(0, 0) }

func stallServer(sink StallChoiceSink) *PlanStatusServer {
	s := NewPlanStatusServer(stubSource{}, nil, nil, stubClock{})
	if sink != nil {
		s = s.WithStallChoice(sink)
	}
	return s
}

func postStall(s *PlanStatusServer, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/stall/choice", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.serveStallChoice(rec, req)
	return rec
}

func TestServeStallChoiceRejectsNonPost(t *testing.T) {
	rec := postStall(stallServer(&fakeStallSink{pending: true}), http.MethodGet, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET should be 405, got %d", rec.Code)
	}
}

func TestServeStallChoiceUnavailableWithoutSink(t *testing.T) {
	rec := postStall(stallServer(nil), http.MethodPost, `{"decision":"cancel"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no sink should be 503, got %d", rec.Code)
	}
}

func TestServeStallChoiceRejectsInvalidJSON(t *testing.T) {
	rec := postStall(stallServer(&fakeStallSink{pending: true}), http.MethodPost, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body should be 400, got %d", rec.Code)
	}
}

func TestServeStallChoiceRejectsUnknownDecision(t *testing.T) {
	rec := postStall(stallServer(&fakeStallSink{pending: true}), http.MethodPost, `{"decision":"bogus"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown decision should be 400, got %d", rec.Code)
	}
}

func TestServeStallChoiceRejectsOtherWithBlankComment(t *testing.T) {
	rec := postStall(stallServer(&fakeStallSink{pending: true}), http.MethodPost, `{"decision":"other","comment":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("other with a blank comment should be 400, got %d", rec.Code)
	}
}

func TestServeStallChoiceRelaysExactDecisionAndComment(t *testing.T) {
	sink := &fakeStallSink{pending: true}
	rec := postStall(stallServer(sink), http.MethodPost, `{"decision":"other","comment":"skip SQLite"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a valid choice should be 202, got %d", rec.Code)
	}
	if sink.calls != 1 {
		t.Fatalf("expected exactly one relay to the sink, got %d", sink.calls)
	}
	if sink.decision != models.StallDecisionOther || sink.comment != "skip SQLite" {
		t.Fatalf("expected the exact decision and comment relayed, got %q / %q", sink.decision, sink.comment)
	}
}

func TestServeStallChoiceConflictsWhenNoWaitPending(t *testing.T) {
	rec := postStall(stallServer(&fakeStallSink{pending: false}), http.MethodPost, `{"decision":"accept-worker"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("no parked run should be 409, got %d", rec.Code)
	}
}
