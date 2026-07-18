// Package speech owns FAIRY's Volcengine voice-clone HTTP boundary.
//
// It stores redacted voice-clone settings, keeps API credentials in the secret
// store, and sends train/query/upgrade HTTP requests with sanitized errors. It
// does not synthesize or play audio in the speech bubble; the current runtime
// only manages voice-clone speaker state.
package speech
