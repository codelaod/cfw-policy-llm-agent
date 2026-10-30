package workload

import (
	"math"
	"testing"
	"time"
)

func TestLegacyDefaultsAreExplicitAndPhaseBudgetsClose(t *testing.T) {
	audit := Default("audit")
	if audit.QueueSeconds != 120 || audit.ExecutionSeconds != 240 || audit.CollectionSeconds != 90 || audit.RulesSeconds != 20 || audit.ModelSeconds != 120 || audit.FinalizeSeconds != 10 || audit.ModelRequestSeconds != 30 || audit.ModelLogicalCalls != 6 || audit.ModelHTTPAttempts != 6 || audit.CloudHTTPAttempts != 100 || audit.MaxQueryCells != 50000 {
		t.Fatal("legacy facts changed")
	}
	for _, kind := range []string{"audit", "assistant_turn", "collect", "connection_check"} {
		if err := Default(kind).Validate(kind); err != nil {
			t.Fatal(kind, err)
		}
	}
	if Default("assistant_turn").ModelLogicalCalls != 4 || Default("collect").ExecutionSeconds != 90 || Default("connection_check").CloudHTTPAttempts != 6 {
		t.Fatal("kind defaults were conflated")
	}
}
func TestExplicitProfilesCanExceedLegacyCapsWithoutBecomingDefaults(t *testing.T) {
	p := Default("audit")
	p.ExecutionSeconds = 600
	p.CollectionSeconds = 180
	p.RulesSeconds = 40
	p.ModelSeconds = 360
	p.FinalizeSeconds = 20
	p.ModelRequestSeconds = 90
	p.ModelLogicalCalls = 12
	p.ModelHTTPAttempts = 16
	p.CloudHTTPAttempts = 1000
	if err := p.Validate("audit"); err != nil {
		t.Fatal("explicit profile still hidden behind legacy cap", err)
	}
	if Default("audit").ModelRequestSeconds != 30 {
		t.Fatal("explicit profile expanded defaults")
	}
}
func TestLimitsRejectNegativeIncoherentAndOverflowValues(t *testing.T) {
	base := Default("audit")
	for name, change := range map[string]func(*Limits){"negative": func(p *Limits) { p.CloudHTTPAttempts = -1 }, "zero-execution": func(p *Limits) { p.ExecutionSeconds = 0 }, "stage-sum": func(p *Limits) { p.ModelSeconds = 241 }, "request-stage": func(p *Limits) { p.ModelRequestSeconds = 121 }, "tool-batch": func(p *Limits) { p.ToolBatch = p.ToolCalls + 1 }, "asset-size": func(p *Limits) { p.AssetsPerBatch = 0 }, "policy-size": func(p *Limits) { p.MaxPolicies = 0 }, "cells": func(p *Limits) { p.MaxQueryCells = 0 }, "root-overflow": func(p *Limits) { p.QueueSeconds = math.MaxInt }, "stage-overflow": func(p *Limits) { p.CollectionSeconds = math.MaxInt }} {
		t.Run(name, func(t *testing.T) {
			p := base
			change(&p)
			if err := p.Validate("audit"); err == nil {
				t.Fatal("invalid limits accepted")
			}
		})
	}
	if _, err := Seconds(math.MaxInt); err == nil {
		t.Fatal("duration overflow accepted")
	}
	if d, err := Seconds(30); err != nil || d != 30*time.Second {
		t.Fatal("valid duration changed")
	}
}
func TestZeroSemanticsAndPrivateCeilingsStayStrict(t *testing.T) {
	p := Default("audit")
	p.QueueSeconds = 0
	p.CloudHTTPAttempts = 0
	p.ModelHTTPAttempts = 0
	if err := p.Validate("audit"); err != nil {
		t.Fatal("explicit no-wait/no-request profile rejected", err)
	}
	p = Default("audit")
	zero, three, ten := 0, 3, 10
	got, err := p.Constrain("audit", Ceilings{ModelHTTPAttempts: &zero, ModelLogicalCalls: &three, ModelRequestSeconds: &ten, CloudHTTPAttempts: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelHTTPAttempts != 0 || got.CloudHTTPAttempts != 0 || got.ModelLogicalCalls != 3 || got.ModelRequestSeconds != 10 || got.MaxPolicies != p.MaxPolicies {
		t.Fatal("explicit zero or absent ceiling was defaulted")
	}
	tooLarge := 999
	got, err = p.Constrain("audit", Ceilings{ModelHTTPAttempts: &tooLarge})
	if err != nil || got.ModelHTTPAttempts != p.ModelHTTPAttempts {
		t.Fatal("ceiling expanded profile")
	}
	negative := -1
	if _, err = p.Constrain("audit", Ceilings{CloudHTTPAttempts: &negative}); err == nil {
		t.Fatal("invalid ceiling accepted")
	}
}

func TestModelWithPositiveAttemptsCannotUseZeroRequestTimeout(t *testing.T) {
	p := Default("audit")
	p.ModelRequestSeconds = 0
	if err := p.Validate("audit"); err == nil {
		t.Fatal("zero per-request timeout remained executable")
	}
	zero := 0
	if _, err := Default("audit").Constrain("audit", Ceilings{ModelRequestSeconds: &zero}); err == nil {
		t.Fatal("zero timeout ceiling acquired a default")
	}
}
