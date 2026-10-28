package outbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reviewReservationLedger struct {
	mu            sync.Mutex
	count         int
	secondEntered chan struct{}
	releaseSecond chan struct{}
}

func (l *reviewReservationLedger) ReserveAttempt(ctx context.Context, _, _, _, _ string) (string, error) {
	l.mu.Lock()
	l.count++
	n := l.count
	l.mu.Unlock()
	if n == 2 {
		close(l.secondEntered)
		select {
		case <-l.releaseSecond:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return strconv.Itoa(n), nil
}
func (l *reviewReservationLedger) RecordAttemptResult(_ context.Context, id, _, _ string, _ int, _ int64) error {
	if id == "1" {
		return errors.New("controlled synthetic record failure")
	}
	return nil
}

func TestReviewE3RecordFailureClosesSendAfterConcurrentReservationWait(t *testing.T) {
	var physical atomic.Int32
	firstArrived := make(chan struct{})
	releaseFirst := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if physical.Add(1) == 1 {
			close(firstArrived)
			select {
			case <-releaseFirst:
			case <-r.Context().Done():
				return
			}
		}
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	ledger := &reviewReservationLedger{secondEntered: make(chan struct{}), releaseSecond: make(chan struct{})}
	manager, err := NewManager(ledger, Config{Revision: "review-v1", Sources: []Source{{Ref: "configured-model", Class: "model", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []Route{{Method: "POST", Path: "/v1/chat/completions"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	done := make(chan error, 2)
	send := func(job string) {
		client, err := manager.Client(context.Background(), job, "configured-model")
		if err != nil {
			done <- err
			return
		}
		req, _ := http.NewRequest("POST", upstream.URL+"/v1/chat/completions", strings.NewReader(`{}`))
		response, err := client.Do(req)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}
	go send("review-first")
	select {
	case <-firstArrived:
	case <-time.After(time.Second):
		t.Fatal("first physical call did not arrive")
	}
	go send("review-second")
	select {
	case <-ledger.secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second attempt did not wait in reservation")
	}
	close(releaseFirst)
	select {
	case err = <-done:
		if ErrorCode(err) != "ATTEMPT_RECORD_FAILED" {
			t.Fatal("first record failure was lost", ErrorCode(err))
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	close(ledger.releaseSecond)
	select {
	case err = <-done:
		if ErrorCode(err) != "ATTEMPT_RECORD_FAILED" {
			t.Errorf("request admitted after gate closure: %s", ErrorCode(err))
		}
	case <-time.After(time.Second):
		t.Fatal("second request did not finish")
	}
	if physical.Load() != 1 {
		t.Fatalf("closed manager still sent %d physical requests; expected only the already sent first request", physical.Load())
	}
}

func TestReviewE3DuplicateJSONActionCannotHideDisallowedOperation(t *testing.T) {
	var physical atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { physical.Add(1); io.WriteString(w, `{}`) }))
	defer upstream.Close()
	ledger := &testLedger{maximum: 3}
	_, client := e3Manager(t, upstream.URL, ledger, Source{Ref: "cloud-cfw", Class: "cloud", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []Route{{Method: "POST", Path: "/", Actions: []string{"DescribeNatFirewallList"}}}})
	request := e3Request(t, context.Background(), "POST", upstream.URL+"/", `{"Action":"DeleteNatFirewall","Action":"DescribeNatFirewallList"}`)
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if ErrorCode(err) != "DESTINATION_REFUSED" || physical.Load() != 0 || ledger.count() != 0 {
		t.Fatalf("duplicate action selectors bypassed read-only validation: code=%s physical=%d reservations=%d", ErrorCode(err), physical.Load(), ledger.count())
	}
}
