package queue

import (
	"testing"

	"github.com/agentforge/agentforge/pkg/model"
)

func TestGroupIDForMessage(t *testing.T) {
	cases := []struct {
		name string
		msg  *model.SQSMessage
		want string
	}{
		{
			name: "task and run",
			msg:  &model.SQSMessage{TaskID: "task_1", RunID: "run_1", TenantID: "tnt_1"},
			want: "task_1#run_1",
		},
		{
			name: "task only",
			msg:  &model.SQSMessage{TaskID: "task_1", TenantID: "tnt_1"},
			want: "task_1",
		},
		{
			name: "run only",
			msg:  &model.SQSMessage{RunID: "run_1", TenantID: "tnt_1"},
			want: "run_1",
		},
		{
			name: "tenant fallback",
			msg:  &model.SQSMessage{TenantID: "tnt_1"},
			want: "tnt_1",
		},
		{
			name: "default fallback",
			msg:  &model.SQSMessage{},
			want: "default",
		},
		{
			name: "nil message",
			msg:  nil,
			want: "default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupIDForMessage(tc.msg); got != tc.want {
				t.Fatalf("group id: expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestDedupeIDForMessage(t *testing.T) {
	msg := &model.SQSMessage{
		TaskID:    "task_1",
		RunID:     "run_1",
		DedupeKey: "custom_key",
	}
	if got := dedupeIDForMessage(msg); got != "custom_key" {
		t.Fatalf("expected explicit dedupe key, got %q", got)
	}

	msg.DedupeKey = ""
	if got := dedupeIDForMessage(msg); got != "task_1#run_1" {
		t.Fatalf("expected default dedupe key, got %q", got)
	}

	if got := dedupeIDForMessage(nil); got != "" {
		t.Fatalf("expected empty dedupe key for nil message, got %q", got)
	}
}
