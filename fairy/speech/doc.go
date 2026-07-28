// Package speech owns FAIRY's Volcengine voice-clone and TTS HTTP boundary.
//
// It stores redacted voice-clone settings, keeps API credentials in the secret
// store, and sends train/query/upgrade/synthesize HTTP requests with sanitized
// errors. Audio playback remains a Surface responsibility.
package speech
