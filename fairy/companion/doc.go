// Package companion owns FAIRY's conversation runtime.
//
// It coordinates prompt construction, memory/model/search collaborators,
// turn lifecycle, reply chain compilation, speech text events, and extraction
// scheduling for explicit user turns. It does not own Wails windows, tray
// wiring, persisted configuration stores, or provider-specific HTTP clients;
// those dependencies are injected by the app composition package.
package companion
