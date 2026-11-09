package agent

import (
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// The agent distinguishes three classes of model failure so the HTTP layer can
// return the right status and the frontend can show the right message:
//
//   - ErrModelBusy: the model is throttling (rate/quota). Retryable soon.
//   - ErrModelUnavailable: the model could not be reached at all, for example
//     missing or expired credentials, or a network/service outage. Retryable
//     later, but not from the same request.
//   - ErrModelError: the request reached the model but failed for another
//     reason (validation, authorization, an internal model error). Not simply
//     retryable; usually indicates a bad request or a model-side problem.
//
// Every model error returned by Respond wraps exactly one of these sentinels
// plus the underlying AWS error, so callers can match with errors.Is and still
// log the original cause.
var (
	// ErrModelBusy reports that the model is throttling the request.
	ErrModelBusy = errors.New("model busy")
	// ErrModelUnavailable reports that the language model could not be reached
	// (for example missing or expired cloud credentials, or a service outage),
	// as opposed to a problem with the merchant's request.
	ErrModelUnavailable = errors.New("model unavailable")
	// ErrModelError reports that the model rejected or failed the request for a
	// reason other than throttling or unreachability.
	ErrModelError = errors.New("model error")
)

// classifyModelError maps a Bedrock Converse error onto one of the model-error
// sentinels. It inspects the AWS SDK's typed errors first, then falls back to
// transport and credential heuristics, and finally to ErrModelError.
//
// Classification rules (checked in order):
//   - Throttling / quota exhaustion -> ErrModelBusy.
//   - Service unavailable, model not ready, or the request never reached the
//     service (transport/connection failure), or a credential-resolution
//     failure -> ErrModelUnavailable.
//   - Any other typed API error (validation, access denied, internal, etc.)
//     or an unrecognised error -> ErrModelError.
func classifyModelError(err error) error {
	if err == nil {
		return nil
	}

	// Throttling and quota: the model is reachable but rejecting load.
	var throttle *types.ThrottlingException
	var quota *types.ServiceQuotaExceededException
	if errors.As(err, &throttle) || errors.As(err, &quota) {
		return ErrModelBusy
	}

	// Service-side unavailability that is purely transient.
	var unavailable *types.ServiceUnavailableException
	var notReady *types.ModelNotReadyException
	if errors.As(err, &unavailable) || errors.As(err, &notReady) {
		return ErrModelUnavailable
	}

	// A typed API error that is none of the above reached the service, so the
	// model is available; the request itself failed. Classify by code to keep
	// throttling robust even if the concrete type ever changes, otherwise treat
	// it as a request/model error.
	var api smithy.APIError
	if errors.As(err, &api) {
		code := api.ErrorCode()
		if strings.Contains(code, "Throttl") || code == "TooManyRequestsException" {
			return ErrModelBusy
		}
		return ErrModelError
	}

	// No typed API error in the chain. Either the request never reached the
	// service (connection failure) or credentials could not be resolved; both
	// mean the model was effectively unavailable for this request.
	var sendErr *smithyhttp.RequestSendError
	if errors.As(err, &sendErr) {
		return ErrModelUnavailable
	}
	if isCredentialError(err) {
		return ErrModelUnavailable
	}

	return ErrModelError
}

// isCredentialError reports whether err looks like a failure to resolve AWS
// credentials. The SDK surfaces these as plain wrapped errors rather than a
// typed API error, so a string check is the available signal.
func isCredentialError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "credential") ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "no ec2 imds role found") ||
		strings.Contains(msg, "failed to refresh cached credentials")
}
