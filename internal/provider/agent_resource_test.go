package provider

import (
	"errors"
	"net/http"
	"testing"

	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

func TestIsArchivedAgentError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "missing agent",
			err:  &client.HTTPError{StatusCode: http.StatusNotFound},
			want: true,
		},
		{
			name: "already archived conflict",
			err: &client.HTTPError{
				StatusCode: http.StatusConflict,
				Body:       `{"error":"agent is already archived"}`,
			},
			want: true,
		},
		{
			name: "other conflict",
			err: &client.HTTPError{
				StatusCode: http.StatusConflict,
				Body:       `{"error":"agent has active tasks"}`,
			},
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("network unavailable"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isArchivedAgentError(tt.err); got != tt.want {
				t.Fatalf("isArchivedAgentError() = %v, want %v", got, tt.want)
			}
		})
	}
}
