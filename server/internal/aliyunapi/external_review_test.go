package aliyunapi

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestReviewIdentityTypeCannotHideRootOrConflictingARN(t *testing.T) {
	for _, arn := range []string{"acs:ram::1234567890123456:root", "acs:ram::1234567890123456:", "acs:sts::1234567890123456:assumed-role/role/session"} {
		t.Run(arn, func(t *testing.T) {
			r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"AccountId":"1234567890123456","IdentityType":"RAMUser","Arn":"`+arn+`","PrincipalId":"synthetic"}`)
			}))
			if err := r.VerifyIdentity(context.Background()); err == nil {
				t.Fatal("a root, missing-resource, or incompatible ARN was accepted as a verified RAM user")
			}
		})
	}
}

func TestReviewOfficialAssumedRoleUserIdentityIsAccepted(t *testing.T) {
	for _, arn := range []string{"acs:ram::1234567890123456:assumed-role/synthetic-role/synthetic-session", "acs:ram::1234567890123456:role/synthetic-role/synthetic-session"} {
		t.Run(arn, func(t *testing.T) {
			r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"AccountId":"1234567890123456","IdentityType":"AssumedRoleUser","Arn":"`+arn+`","PrincipalId":"synthetic-role:synthetic-session"}`)
			}))
			if err := r.VerifyIdentity(context.Background()); err != nil {
				t.Fatal("official assumed role identity was refused", err)
			}
		})
	}
}
