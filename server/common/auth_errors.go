package common

import (
	"errors"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/gin-gonic/gin"
	pkgerr "github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// AuthFailure describes how an authentication path should treat a failure.
type AuthFailure struct {
	// Code is the API business code (also used as HTTP status for non-JSON auth).
	Code int
	// Message is a client-visible error string.
	Message string
	// CountAsAuthFailure is true when the failure should increment login lockout counters.
	CountAsAuthFailure bool
}

// ClassifyUserLookupError maps user-lookup errors to auth responses.
// Database lock / nested-transaction failures become 503 and must not look like
// invalid credentials; unknown users stay 401; other DB errors become 500.
func ClassifyUserLookupError(err error) AuthFailure {
	return ClassifyAuthError(err)
}

// ClassifyAuthError maps login / credential check errors to auth responses.
func ClassifyAuthError(err error) AuthFailure {
	if err == nil {
		return AuthFailure{}
	}
	if errs.IsDatabaseBusy(err) {
		return AuthFailure{
			Code:               503,
			Message:            model.DatabaseTemporarilyUnavailable,
			CountAsAuthFailure: false,
		}
	}
	if errs.IsRecordNotFound(err) ||
		errors.Is(err, errs.EmptyUsername) ||
		errors.Is(err, errs.WrongPassword) ||
		errors.Is(err, errs.EmptyPassword) ||
		errors.Is(err, ErrFailedLdapAuth) ||
		errors.Is(pkgerr.Cause(err), ErrFailedLdapAuth) {
		return AuthFailure{
			Code:               401,
			Message:            model.InvalidUsernameOrPassword,
			CountAsAuthFailure: true,
		}
	}
	return AuthFailure{
		Code:               500,
		Message:            "internal server error",
		CountAsAuthFailure: false,
	}
}

// ErrorUserLookupResp writes a JSON API error for a failed user lookup.
func ErrorUserLookupResp(c *gin.Context, err error) AuthFailure {
	failure := ClassifyUserLookupError(err)
	if failure.Code >= 500 {
		// Log the driver error server-side. Unauthenticated clients only see
		// the generic message from ClassifyAuthError.
		log.Errorf("auth lookup failed: %+v", err)
	}
	ErrorStrResp(c, failure.Message, failure.Code)
	return failure
}

// ErrorDatabaseOrInternal maps lock contention to 503 and hides other
// database errors from the client.
func ErrorDatabaseOrInternal(c *gin.Context, err error) {
	if errs.IsDatabaseBusy(err) {
		ErrorUserLookupResp(c, err)
		return
	}
	log.Errorf("internal auth error: %+v", err)
	ErrorStrResp(c, "internal server error", 500)
}
