package response

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"

	"ambigo-backend/internal/logger"

	"github.com/go-playground/validator/v10"
)

// ─────────────────────────────────────────────────────────────────────────────
// Typed error codes
// ─────────────────────────────────────────────────────────────────────────────

// ErrorCode is a machine-readable string that API consumers can switch on.
// It is intentionally a named string type so the compiler catches typos.
type ErrorCode string

const (
	// ErrMissingContentType is returned when the Content-Type header is absent
	// or is not "application/json". Prevents servers from accepting accidental
	// form-encoded or plain-text payloads silently.
	ErrMissingContentType ErrorCode = "MISSING_CONTENT_TYPE"

	// ErrMissingBody is returned when the request body is completely empty.
	// Helps clients distinguish between "I forgot the body" and "my JSON is bad".
	ErrMissingBody ErrorCode = "MISSING_BODY"

	// ErrBodyTooLarge is returned when the body exceeds the configured byte limit.
	// Prevents memory exhaustion attacks and clearly signals the client to shrink
	// its payload.
	ErrBodyTooLarge ErrorCode = "BODY_TOO_LARGE"

	// ErrMalformedJSON is returned for JSON syntax errors (e.g. unclosed braces,
	// bare strings, trailing commas). Distinct from type mismatches.
	ErrMalformedJSON ErrorCode = "MALFORMED_JSON"

	// ErrInvalidUTF8 is returned when the body contains invalid UTF-8 byte
	// sequences or corrupted encoding. Helps diagnose garbled payloads.
	ErrInvalidUTF8 ErrorCode = "INVALID_UTF8"

	// ErrTypeMismatch is returned when a JSON value cannot be decoded into the
	// target Go type (e.g. an object where a string is expected, or a number
	// where a boolean is expected).
	ErrTypeMismatch ErrorCode = "TYPE_MISMATCH"

	// ErrUnknownField is returned when the JSON body contains a key that does
	// not map to any field in the target struct. Prevents clients from silently
	// sending data that the server ignores.
	ErrUnknownField ErrorCode = "UNKNOWN_FIELD"

	// ErrMultipleObjects is returned when the body contains more than one
	// top-level JSON value (e.g. two objects concatenated). Most REST APIs
	// expect exactly one JSON document per request.
	ErrMultipleObjects ErrorCode = "MULTIPLE_JSON_OBJECTS"

	// ErrMissingRequiredField is returned when validation finds that a required
	// field is absent or empty after successful JSON decoding.
	ErrMissingRequiredField ErrorCode = "MISSING_REQUIRED_FIELD"

	// ErrInvalidJSON is a catch-all for any other JSON decoding failure that
	// does not match the more specific codes above.
	ErrInvalidJSON ErrorCode = "INVALID_JSON"
)

// ─────────────────────────────────────────────────────────────────────────────
// Error response envelope
// ─────────────────────────────────────────────────────────────────────────────

// APIError is the structured error payload written to the response body.
//
// Example JSON:
//
//	{
//	    "success": false,
//	    "error": {
//	        "code":    "TYPE_MISMATCH",
//	        "message": "Invalid value for field 'portrait_image'.",
//	        "field":   "portrait_image",
//	        "details": "Expected string but received object."
//	    }
//	}
type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Field   string    `json:"field,omitempty"`
	Details string    `json:"details,omitempty"`
}

// writeAPIError serialises an APIError envelope and logs the raw internal
// error to the server. Clients never see the raw Go error string.
func writeAPIError(w http.ResponseWriter, r *http.Request, httpStatus int, ae APIError, internalErr error) {
	// Server-side structured log — safe to include internal details here.
	log := logger.Ctx(r.Context())
	event := log.Warn().
		Str("error_code", string(ae.Code)).
		Int("http_status", httpStatus)
	if ae.Field != "" {
		event = event.Str("field", ae.Field)
	}
	if internalErr != nil {
		event = event.Err(internalErr)
	}
	event.Msg("JSON decode error")

	// Client-facing response — no raw Go errors exposed.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"error":   ae,
		"detail":  ae.Message, // Backward-compatible fallback for legacy frontend expectations
		"code":    httpStatus, // Backward-compatible fallback for legacy status code parsing
	})
}


// ─────────────────────────────────────────────────────────────────────────────
// DecodeJSONBody — reusable generic decode helper
// ─────────────────────────────────────────────────────────────────────────────

// defaultMaxBytes is the default per-handler body limit (1 MB), matching the
// global BodyLimit middleware for defense-in-depth.
const defaultMaxBytes int64 = 1 << 20 // 1 MB

// decodeValidator is a package-level validator instance reused across requests.
// RegisterTagNameFunc instructs it to use the `json` struct tag as the field
// name in all ValidationError messages, so fe.Field() returns "portrait_image"
// (the JSON key) instead of "PortraitImage" (the Go identifier).
var decodeValidator = func() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" || name == "" {
			return fld.Name // fallback to Go field name
		}
		return name
	})
	return v
}()

// DecodeJSONBody decodes a JSON request body into a value of type T, applying
// all production-grade validations in sequence:
//
//  1. Content-Type must be "application/json" (or contain it).
//  2. Body size is capped at maxBytes (pass 0 to use the 1 MB default).
//  3. Unknown JSON fields are rejected.
//  4. Every decode error is mapped to a typed ErrorCode.
//  5. A second JSON object after the first is rejected.
//  6. Struct-level validation is run via go-playground/validator.
//
// Returns (value, true) on success, or (zero, false) if an error response has
// already been written to w. The caller should return immediately on false.
//
// Usage:
//
//	req, ok := response.DecodeJSONBody[auth.VerificationUpdateRequest](w, r, 0)
//	if !ok {
//	    return
//	}
func DecodeJSONBody[T any](w http.ResponseWriter, r *http.Request, maxBytes int64) (T, bool) {
	var zero T

	// ── 1. Validate Content-Type ──────────────────────────────────────────────
	// Why: Without this check a client can accidentally POST form data or plain
	// text and get confusing decode errors. HTTP 415 Unsupported Media Type is
	// the correct status for a bad Content-Type.
	ct := r.Header.Get("Content-Type")
	if ct == "" || !strings.Contains(strings.ToLower(ct), "application/json") {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, APIError{
			Code:    ErrMissingContentType,
			Message: "Content-Type must be application/json.",
			Details: fmt.Sprintf("Received: %q", ct),
		}, nil)
		return zero, false
	}

	// ── 2. Cap body size ──────────────────────────────────────────────────────
	// Why: Without a limit, a malicious client can send an arbitrarily large
	// body, exhausting server memory. http.MaxBytesReader enforces the limit at
	// the io.Reader level before any bytes are parsed.
	limit := maxBytes
	if limit <= 0 {
		limit = defaultMaxBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	// ── 3. Create decoder with strict field checking ──────────────────────────
	// Why: DisallowUnknownFields causes the decoder to error if the JSON body
	// contains keys not present in the target struct. This prevents clients from
	// silently sending data that the server discards, catching integration bugs
	// early.
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	// ── 4. Decode and map errors ──────────────────────────────────────────────
	var dst T
	if err := dec.Decode(&dst); err != nil {
		mapDecodeError(w, r, err)
		return zero, false
	}

	// ── 5. Check for trailing JSON objects ────────────────────────────────────
	// Why: A body like `{"a":"b"}{"c":"d"}` is syntactically valid per the JSON
	// spec but almost always indicates a client bug. Rejecting it explicitly
	// prevents silent data loss where only the first object is parsed.
	if dec.More() {
		writeAPIError(w, r, http.StatusBadRequest, APIError{
			Code:    ErrMultipleObjects,
			Message: "Request body must contain exactly one JSON object.",
			Details: "Unexpected data found after the first JSON value.",
		}, errors.New("multiple JSON objects in request body"))
		return zero, false
	}

	// ── 6. Struct validation ──────────────────────────────────────────────────
	// Why: JSON decoding succeeds even when required fields are missing or hold
	// zero values. Struct tags (validate:"required") make business rules
	// explicit and machine-enforceable.
	if err := decodeValidator.Struct(dst); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) && len(ve) > 0 {
			first := ve[0]
			fieldName := toJSONFieldName(first)
			writeAPIError(w, r, http.StatusUnprocessableEntity, APIError{
				Code:    ErrMissingRequiredField,
				Message: fmt.Sprintf("Required field '%s' is missing or empty.", fieldName),
				Field:   fieldName,
				Details: fmt.Sprintf("Validation failed on rule: %s.", first.Tag()),
			}, err)
			return zero, false
		}
		writeAPIError(w, r, http.StatusUnprocessableEntity, APIError{
			Code:    ErrMissingRequiredField,
			Message: "One or more required fields are missing or invalid.",
		}, err)
		return zero, false
	}

	return dst, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// mapDecodeError translates the raw error from json.Decoder into a typed
// APIError and writes it to w. Internal error strings are never exposed to the
// client — they are logged server-side only.
func mapDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	// ── Empty body ────────────────────────────────────────────────────────────
	// io.EOF is returned by the decoder when the body is completely empty.
	if errors.Is(err, io.EOF) {
		writeAPIError(w, r, http.StatusBadRequest, APIError{
			Code:    ErrMissingBody,
			Message: "Request body must not be empty.",
		}, err)
		return
	}

	// ── Body too large ────────────────────────────────────────────────────────
	// http.MaxBytesReader wraps the underlying error in *http.MaxBytesError
	// (Go 1.19+). We check for both the typed error and the legacy string.
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeAPIError(w, r, http.StatusRequestEntityTooLarge, APIError{
			Code:    ErrBodyTooLarge,
			Message: fmt.Sprintf("Request body exceeds the maximum allowed size of %d bytes.", maxBytesErr.Limit),
		}, err)
		return
	}
	// Fallback for older Go versions or non-typed wrapping.
	if strings.Contains(err.Error(), "http: request body too large") {
		writeAPIError(w, r, http.StatusRequestEntityTooLarge, APIError{
			Code:    ErrBodyTooLarge,
			Message: "Request body is too large.",
		}, err)
		return
	}

	// ── JSON syntax error ─────────────────────────────────────────────────────
	// json.SyntaxError covers malformed JSON: unclosed brackets, bare keys,
	// trailing commas, etc.
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		writeAPIError(w, r, http.StatusBadRequest, APIError{
			Code:    ErrMalformedJSON,
			Message: "Request body contains malformed JSON.",
			Details: fmt.Sprintf("Syntax error at byte offset %d.", syntaxErr.Offset),
		}, err)
		return
	}

	// ── Unexpected EOF (truncated body) ───────────────────────────────────────
	// io.ErrUnexpectedEOF means the body was cut off mid-JSON (e.g. a network
	// error during upload). Separate from an outright empty body.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		writeAPIError(w, r, http.StatusBadRequest, APIError{
			Code:    ErrMalformedJSON,
			Message: "Request body contains incomplete JSON (body was truncated).",
		}, err)
		return
	}

	// ── Type mismatch ─────────────────────────────────────────────────────────
	// json.UnmarshalTypeError is returned when a JSON value cannot be stored
	// in the target Go type (e.g. object into string, number into bool).
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		writeAPIError(w, r, http.StatusBadRequest, APIError{
			Code:    ErrTypeMismatch,
			Message: fmt.Sprintf("Invalid value for field '%s'.", typeErr.Field),
			Field:   typeErr.Field,
			Details: fmt.Sprintf("Expected %s but received %s.", typeErr.Type.String(), typeErr.Value),
		}, err)
		return
	}

	// ── Unknown field ─────────────────────────────────────────────────────────
	// DisallowUnknownFields() returns a plain error with a prefix string.
	// There is no typed sentinel, so we match by prefix.
	if strings.HasPrefix(err.Error(), "json: unknown field") {
		// Extract the field name from the error string: `json: unknown field "foo"`
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		field = strings.Trim(field, `"`)
		writeAPIError(w, r, http.StatusBadRequest, APIError{
			Code:    ErrUnknownField,
			Message: fmt.Sprintf("Unknown field '%s' in request body.", field),
			Field:   field,
			Details: "Remove unknown fields and resubmit.",
		}, err)
		return
	}

	// ── Invalid UTF-8 / corrupted bytes ──────────────────────────────────────
	// json.InvalidUTF8Error was removed in Go 1.2; the decoder now produces a
	// json.SyntaxError for invalid UTF-8. We add an extra utf8 validity guard
	// here as a belt-and-suspenders check for payloads that slip through.
	errStr := err.Error()
	if strings.Contains(errStr, "invalid character") || strings.Contains(errStr, "invalid UTF-8") {
		// Try to distinguish a pure encoding problem from a general syntax error
		// by checking whether the raw error message mentions UTF-8.
		if strings.Contains(errStr, "UTF-8") || strings.Contains(errStr, "utf-8") {
			writeAPIError(w, r, http.StatusBadRequest, APIError{
				Code:    ErrInvalidUTF8,
				Message: "Request body contains invalid UTF-8 encoding.",
			}, err)
			return
		}
	}

	// ── Null into non-pointer string ──────────────────────────────────────────
	// When a JSON null is decoded into a non-pointer Go string, the decoder
	// does NOT error — it simply leaves the field as its zero value ("").
	// This case is therefore caught downstream by struct validation
	// (validate:"required") rather than here. No special case needed.

	// ── Catch-all ─────────────────────────────────────────────────────────────
	writeAPIError(w, r, http.StatusBadRequest, APIError{
		Code:    ErrInvalidJSON,
		Message: "Failed to parse request body.",
	}, err)
}

// toJSONFieldName returns the JSON field name from a validator.FieldError.
// Because decodeValidator has a RegisterTagNameFunc that reads `json` struct
// tags, fe.Field() already returns the JSON key (e.g. "portrait_image").
// This helper adds a UTF-8 safety guard and nothing else.
func toJSONFieldName(fe validator.FieldError) string {
	name := fe.Field()
	if !utf8.ValidString(name) {
		return "unknown_field"
	}
	return name
}
