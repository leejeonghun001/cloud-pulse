package alerting

import (
	"context"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/i18n"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestBuildMessage_EnglishTextIsUnchanged(t *testing.T) {
	t.Parallel()
	rule := models.AlertRule{Name: "CPU high", Metric: models.AlertMetricCPU}
	ev := models.AlertEvent{Hostname: "web-1", Metric: models.AlertMetricCPU, Value: 93.5, Threshold: 90, State: models.AlertEventFiring}

	msg := buildMessage(i18n.LangEn, rule, ev, SeverityWarning)
	if msg.Title != "web-1: CPU high" {
		t.Errorf("Title = %q", msg.Title)
	}
	if msg.Text != "CPU high is 93.5% (threshold 90.0%)" {
		t.Errorf("Text = %q", msg.Text)
	}
	if msg.Fields[0].Name != "Host" || msg.Fields[3].Name != "Threshold" {
		t.Errorf("field names = %+v", msg.Fields)
	}

	ev.State = models.AlertEventResolved
	ev.Value = 40
	if got := buildMessage(i18n.LangEn, rule, ev, SeverityResolved).Text; got != "CPU high has recovered: now 40.0% (threshold was 90.0%)" {
		t.Errorf("resolved Text = %q", got)
	}
}

func TestBuildMessage_Korean(t *testing.T) {
	t.Parallel()
	rule := models.AlertRule{Name: "CPU high", Metric: models.AlertMetricCPU}
	ev := models.AlertEvent{Hostname: "web-1", Metric: models.AlertMetricCPU, Value: 93.5, Threshold: 90, State: models.AlertEventFiring}

	msg := buildMessage(i18n.LangKo, rule, ev, SeverityWarning)
	if msg.Text != "CPU high: 현재 93.5% (임계값 90.0%)" {
		t.Errorf("Text = %q", msg.Text)
	}
	if msg.Fields[0].Name != "호스트" {
		t.Errorf("Fields[0].Name = %q", msg.Fields[0].Name)
	}
	// Machine-readable values never change with the language.
	if msg.Fields[1].Value != "cpu" || msg.Fields[4].Value != "firing" {
		t.Errorf("field values = %+v", msg.Fields)
	}
}

func TestBuildMessage_AgentOutdatedHasItsOwnSentence(t *testing.T) {
	t.Parallel()
	rule := models.AlertRule{Name: "Agent behind", Metric: models.AlertMetricAgentOutdated}
	ev := models.AlertEvent{Hostname: "web-1", Metric: models.AlertMetricAgentOutdated, Value: 1, State: models.AlertEventFiring}

	for _, lang := range []i18n.Lang{i18n.LangEn, i18n.LangKo} {
		text := buildMessage(lang, rule, ev, SeverityWarning).Text
		if strings.Contains(text, "threshold") || strings.Contains(text, "임계값") {
			t.Errorf("%s: agent_outdated text exposes the synthetic value: %q", lang, text)
		}
	}
	if got := buildMessage(i18n.LangEn, rule, ev, SeverityWarning).Text; got != "Agent behind: this host's agent is behind the latest release" {
		t.Errorf("en Text = %q", got)
	}
}

func TestBuildUpdateBatchMessage_Korean(t *testing.T) {
	t.Parallel()
	outcome := UpdateBatchOutcome{BatchID: "b-1", Target: "latest", Succeeded: 3, Failed: 1}
	msg := buildUpdateBatchMessage(i18n.LangKo, models.AlertRule{}, outcome)
	if msg.Title != "업데이트 배치 b-1: 1건 실패" {
		t.Errorf("Title = %q", msg.Title)
	}
	if msg.Text != "배치 b-1(대상 latest) 완료: 성공 3건, 실패 1건" {
		t.Errorf("Text = %q", msg.Text)
	}
	en := buildUpdateBatchMessage(i18n.LangEn, models.AlertRule{}, outcome)
	if en.Title != "Update batch b-1: 1 failed" || en.Text != "Batch b-1 (target latest) finished: 3 succeeded, 1 failed." {
		t.Errorf("en = %q / %q", en.Title, en.Text)
	}
}

func TestEngineNotifyLanguage_DefaultsToEnglish(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	if got := e.notifyLanguage(t.Context()); got != i18n.LangEn {
		t.Errorf("nil Language option = %q, want en", got)
	}
	e.language = func(_ context.Context) i18n.Lang { return i18n.LangKo }
	if got := e.notifyLanguage(t.Context()); got != i18n.LangKo {
		t.Errorf("Language option = %q, want ko", got)
	}
}
