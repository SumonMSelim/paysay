package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/SumonMSelim/paysay/internal/i18n"
	"github.com/SumonMSelim/paysay/internal/paypal"
)

func TestClassifyModelError(t *testing.T) {
	strptr := func(s string) *string { return &s }
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"throttling typed", &types.ThrottlingException{Message: strptr("slow down")}, ErrModelBusy},
		{"service quota exceeded", &types.ServiceQuotaExceededException{Message: strptr("quota")}, ErrModelBusy},
		{"throttling by code", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "x"}, ErrModelBusy},
		{"service unavailable", &types.ServiceUnavailableException{Message: strptr("down")}, ErrModelUnavailable},
		{"model not ready", &types.ModelNotReadyException{Message: strptr("warming up")}, ErrModelUnavailable},
		{"transport send failure", &smithyhttp.RequestSendError{Err: errors.New("dial tcp: connection refused")}, ErrModelUnavailable},
		{"credential resolution failure", errors.New("operation error Bedrock: Converse, get credentials: token expired"), ErrModelUnavailable},
		{"validation error", &types.ValidationException{Message: strptr("bad input")}, ErrModelError},
		{"access denied", &types.AccessDeniedException{Message: strptr("nope")}, ErrModelError},
		{"unknown error", errors.New("something else"), ErrModelError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Wrap in an OperationError to mimic how the SDK surfaces errors, so
			// classification must see through the chain.
			wrapped := &smithy.OperationError{ServiceID: "Bedrock Runtime", OperationName: "Converse", Err: c.err}
			if got := classifyModelError(wrapped); !errors.Is(got, c.want) {
				t.Errorf("classifyModelError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestClassifyModelErrorNil(t *testing.T) {
	if got := classifyModelError(nil); got != nil {
		t.Errorf("classifyModelError(nil) = %v, want nil", got)
	}
}

// typedErrorConverser fails every Converse with a fixed error, so the full
// Respond path can be checked end to end.
type typedErrorConverser struct{ err error }

func (c typedErrorConverser) Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return nil, c.err
}

// TestRespondClassifiesBusy checks the full Respond path tags a throttled model
// as ErrModelBusy while preserving the underlying error in the chain.
func TestRespondClassifiesBusy(t *testing.T) {
	thr := &types.ThrottlingException{Message: func() *string { s := "slow down"; return &s }()}
	ag := New(typedErrorConverser{err: thr}, paypal.NewMockProvider(""), Options{ModelID: "m", MaxTokens: 256})
	_, err := ag.Respond(context.Background(), nil, "hi", i18n.MustGet("en-US"))
	if !errors.Is(err, ErrModelBusy) {
		t.Fatalf("err = %v, want ErrModelBusy", err)
	}
	var got *types.ThrottlingException
	if !errors.As(err, &got) {
		t.Errorf("underlying ThrottlingException not preserved in chain: %v", err)
	}
}
