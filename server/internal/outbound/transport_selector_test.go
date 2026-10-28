package outbound

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRPCSelectorsMustAgreeAcrossEveryWireOccurrence(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { physical.Add(1); io.WriteString(w, `{}`) }))
	defer server.Close()
	ledger := &testLedger{maximum: 8}
	_, client := e3Manager(t, server.URL, ledger, Source{Ref: "cloud-cfw", Class: "cloud", Origin: server.URL, AllowLoopbackHTTP: true, Routes: []Route{{Method: "POST", Path: "/", Actions: []string{"DescribeNatFirewallList", "DescribeNatFirewallControlPolicy"}}}})
	for _, test := range []struct{ name, query, header, body string }{
		{"write first duplicate", "", "", `{"Action":"DeleteNatFirewall","Action":"DescribeNatFirewallList"}`},
		{"write last duplicate", "", "", `{"Action":"DescribeNatFirewallList","Action":"DeleteNatFirewall"}`},
		{"escaped conflicting key", "", "", `{"\u0041ction":"DeleteNatFirewall","Action":"DescribeNatFirewallList"}`},
		{"case variant conflicting key", "", "", `{"action":"DeleteNatFirewall","Action":"DescribeNatFirewallList"}`},
		{"two permitted but different actions", "", "", `{"Action":"DescribeNatFirewallList","Action":"DescribeNatFirewallControlPolicy"}`},
		{"query body conflict", "?Action=DescribeNatFirewallList", "", `{"Action":"DescribeNatFirewallControlPolicy"}`},
		{"header body conflict", "", "DescribeNatFirewallList", `{"Action":"DescribeNatFirewallControlPolicy"}`},
		{"query duplicate conflict", "?Action=DescribeNatFirewallList&Action=DescribeNatFirewallControlPolicy", "", `{}`},
		{"array body", "?Action=DescribeNatFirewallList", "", `[]`},
		{"trailing object", "?Action=DescribeNatFirewallList", "", `{} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := e3Request(t, context.Background(), "POST", server.URL+"/"+test.query, test.body)
			if test.header != "" {
				req.Header.Set("X-Acs-Action", test.header)
			}
			response, err := client.Do(req)
			if response != nil {
				response.Body.Close()
			}
			if ErrorCode(err) != "DESTINATION_REFUSED" {
				t.Fatal("ambiguous operation was admitted", ErrorCode(err))
			}
		})
	}
	if ledger.count() != 0 || physical.Load() != 0 {
		t.Fatal("refused selectors consumed admission or physical HTTP", ledger.count(), physical.Load())
	}
	request := e3Request(t, context.Background(), "POST", server.URL+"/?Action=DescribeNatFirewallList", `{"Action":"DescribeNatFirewallList","action":"DescribeNatFirewallList"}`)
	request.Header.Set("X-Acs-Action", "DescribeNatFirewallList")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("identical operation selectors rejected", ErrorCode(err))
	}
	response.Body.Close()
	if ledger.count() != 1 || physical.Load() != 1 {
		t.Fatal("identical selectors did not create exactly one attempt")
	}
}

func TestRPCJSONParsingRejectsNonStringOrMalformedActions(t *testing.T) {
	for _, body := range []string{`{"Action":null}`, `{"Action":["DescribeNatFirewallList"]}`, `{"Action":{}}`, `{"Action":false}`, `{"Action":"DescribeNatFirewallList",}`, `{"Action":"DescribeNatFirewallList"} 0`} {
		values, err := rpcJSONActions([]byte(body))
		// Null is retained as an empty selector and is rejected by the allowlist.
		if err == nil && (len(values) != 1 || strings.TrimSpace(values[0]) != "") {
			t.Fatalf("malformed selector survived parsing: %q", body)
		}
	}
}
