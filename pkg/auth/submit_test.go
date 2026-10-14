package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/mining/collector"
	"github.com/jevlinai/jevlin-go/pkg/mining/scope"
	"github.com/jevlinai/jevlin-go/pkg/mining/spool"
	"github.com/jevlinai/jevlin-go/pkg/wire"
)

const testAckRecordID = "019c7a8e-1b2d-7c3e-8f40-5a6b7c8d9e0f"

func validTestAck() submissionAck {
	return submissionAck{
		SubmissionStatus:     "ACCEPTED",
		ObservationID:        "obsv_019c7a8e3d4f7e50",
		ClientRecordID:       testAckRecordID,
		ReconciliationStatus: "PENDING",
	}
}

func TestClassifySubmissionAck(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*submissionAck)
		want    bool
		wantErr error
	}{
		{name: "accepted pending", want: true},
		{name: "already accepted", mutate: func(a *submissionAck) { a.SubmissionStatus = "ALREADY_ACCEPTED" }, want: true},
		{name: "synthetic duplicate consumed", mutate: func(a *submissionAck) {
			a.SubmissionStatus = ""
			a.ReconciliationStatus = "DUPLICATE"
			a.Reason = "EVIDENCE_ALREADY_CONSUMED"
		}, want: true},
		{name: "synthetic duplicate wrong identity", mutate: func(a *submissionAck) {
			a.SubmissionStatus = ""
			a.ReconciliationStatus = "DUPLICATE"
			a.Reason = "EVIDENCE_ALREADY_CONSUMED"
			a.ClientRecordID = "other-record"
		}, wantErr: ErrAckClientRecordIDMismatch},
		{name: "wrong identity", mutate: func(a *submissionAck) { a.ClientRecordID = "other-record" }, wantErr: ErrAckClientRecordIDMismatch},
		{name: "missing identity", mutate: func(a *submissionAck) { a.ClientRecordID = "" }, wantErr: ErrAckMissingClientRecordID},
		{name: "missing observation", mutate: func(a *submissionAck) { a.ObservationID = "" }, wantErr: ErrAckMissingObservationID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ack := validTestAck()
			if tt.mutate != nil {
				tt.mutate(&ack)
			}
			got, err := classifySubmissionAck(ack, testAckRecordID)
			if got != tt.want {
				t.Fatalf("satisfied = %v, want %v (err=%v)", got, tt.want, err)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}

func TestReadSubmissionAckRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "malformed JSON", body: `{"submission_status":`, wantErr: ErrAckMalformedJSON},
		{name: "trailing second JSON value", body: `{"submission_status":"ACCEPTED"} {}`, wantErr: ErrAckTrailingData},
		{name: "trailing garbage", body: `{"submission_status":"ACCEPTED"} garbage`, wantErr: ErrAckTrailingData},
		{name: "oversized response", body: strings.Repeat("x", maxSubmissionAckBytes+1), wantErr: ErrAckBodyTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readSubmissionAck(strings.NewReader(tt.body))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}

func TestReadSubmissionAckAllowsWhitespaceAfterDocument(t *testing.T) {
	_, err := readSubmissionAck(strings.NewReader("{\"submission_status\":\"ACCEPTED\"} \n\t"))
	if err != nil {
		t.Fatalf("readSubmissionAck: %v", err)
	}
}

type submitRoundTripper func(*http.Request) (*http.Response, error)

func (f submitRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestSubmitter(t *testing.T, body string) *Submitter {
	t.Helper()
	return newTestSubmitterAnswering(t, http.StatusOK, nil, body)
}

// newTestSubmitterAnswering answers every submission with status, header
// and body.
func newTestSubmitterAnswering(t *testing.T, status int, header http.Header, body string) *Submitter {
	t.Helper()
	if header == nil {
		header = make(http.Header)
	}
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proofer, err := NewProofer(key)
	if err != nil {
		t.Fatal(err)
	}
	d := &Discoverer{
		cfg:       DiscoveryConfig{ChainID: "test-chain", SlotID: 7, TTL: time.Hour},
		now:       func() time.Time { return now },
		fetchedAt: now,
		doc:       &wire.DiscoveryDocument{ObservationsEndpoint: "https://as.example/observations"},
	}
	oauth := &OAuthClient{transport: &dpopTransport{
		base: submitRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     header.Clone(),
				Request:    r,
			}, nil
		}),
		proofer: proofer,
	}}
	mining := &MiningClient{discoverer: d, oauth: oauth}
	caps := &CapabilityClient{
		mining: mining,
		holder: &scope.Holder{},
		current: &scope.Context{
			SlotID: 7, TargetEpoch: 1, Capability: "capability", ExpiresAt: now.Add(time.Hour),
		},
	}
	return NewSubmitter(mining, caps)
}

func TestInvalidSubmissionAcknowledgementKeepsSpoolRecord(t *testing.T) {
	valid := `{"submission_status":"ACCEPTED","observation_id":"obsv-1","client_record_id":"` + testAckRecordID + `","reconciliation_status":"PENDING"}`
	tests := []struct {
		name string
		body string
	}{
		{name: "wrong client record id", body: strings.Replace(valid, testAckRecordID, "other-record", 1)},
		{name: "missing client record id", body: strings.Replace(valid, `,"client_record_id":"`+testAckRecordID+`"`, "", 1)},
		{name: "missing observation id", body: strings.Replace(valid, `"observation_id":"obsv-1",`, "", 1)},
		{name: "oversized response", body: strings.Repeat("x", maxSubmissionAckBytes+1)},
		{name: "trailing second value", body: valid + ` {}`},
		{name: "trailing garbage", body: valid + " garbage"},
		{name: "malformed JSON", body: `{"submission_status":`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := newTestSubmitter(t, tt.body)
			dir := t.TempDir()
			sp, err := spool.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := sp.Enqueue(&spool.Record{
				ClientRecordID: testAckRecordID,
				SlotID:         7,
				TargetEpoch:    1,
				Observation:    []byte(`{"client_record_id":"` + testAckRecordID + `"}`),
			}); err != nil {
				t.Fatal(err)
			}
			c := collector.New(sp, sub, collector.Options{MaxAttempts: 0, BaseBackoff: time.Hour, MaxBackoff: time.Hour})
			c.Drain(t.Context())
			pending, err := sp.Pending()
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 1 {
				t.Fatalf("invalid acknowledgement removed record: %+v", pending)
			}
		})
	}
}

func TestValidSubmissionAcknowledgementsRemoveSpoolRecord(t *testing.T) {
	for _, status := range []string{"ACCEPTED", "ALREADY_ACCEPTED"} {
		t.Run(status, func(t *testing.T) {
			body := `{"submission_status":"` + status + `","observation_id":"obsv-1","client_record_id":"` + testAckRecordID + `","reconciliation_status":"PENDING"}`
			sub := newTestSubmitter(t, body)
			sp, err := spool.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := sp.Enqueue(&spool.Record{
				ClientRecordID: testAckRecordID,
				SlotID:         7,
				TargetEpoch:    1,
				Observation:    []byte(`{"client_record_id":"` + testAckRecordID + `"}`),
			}); err != nil {
				t.Fatal(err)
			}
			collector.New(sp, sub, collector.Options{MaxAttempts: 0}).Drain(t.Context())
			pending, err := sp.Pending()
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 {
				t.Fatalf("valid %s acknowledgement retained record: %+v", status, pending)
			}
		})
	}
}

// ADR-MINIS-0026 §2 and §6: the AS refuses a submission from a refused
// address with 451 and one of the region codes, and a 451 is retried later
// — the hold with the Retry-After it carries — never quarantined as
// permanent: the AS decides each attempt afresh, and a held observation is
// accepted when it is resubmitted from an attributable address.
func TestARegionRefusedSubmissionIsRetriedAfterTheASsDelay(t *testing.T) {
	for _, code := range []string{RegionRestricted, RegionIneligible, RegionUnverified} {
		t.Run(code, func(t *testing.T) {
			header := http.Header{}
			if code == RegionUnverified {
				header.Set("Retry-After", "600")
			}
			sub := newTestSubmitterAnswering(t, http.StatusUnavailableForLegalReasons, header,
				`{"error":{"code":"`+code+`","message":"refused under the region policy"}}`)
			satisfied, permanent, retryAfter, err := sub.Submit(t.Context(), &spool.Record{
				ClientRecordID: testAckRecordID, SlotID: 7, TargetEpoch: 1,
				Observation: []byte(`{"client_record_id":"` + testAckRecordID + `"}`),
			})
			if satisfied || permanent || err == nil {
				t.Fatalf("Submit = satisfied %v, permanent %v, err %v; want a retryable failure", satisfied, permanent, err)
			}
			if code == RegionUnverified && retryAfter != 600*time.Second {
				t.Fatalf("retryAfter = %v, want the AS's 600s", retryAfter)
			}
			if !strings.Contains(err.Error(), code) {
				t.Fatalf("the error %q does not name the code", err)
			}
		})
	}
}
