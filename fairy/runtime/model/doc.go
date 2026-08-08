// Package model owns model-provider request construction and transports.
//
// It builds validated OpenAI-compatible Responses and Chat Completions calls,
// streams provider output, exposes model configuration status, and keeps
// provider errors explicit. It does not decide companion behavior or persist
// conversation history; companion and memory own those domains.
package model
