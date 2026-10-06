// Package version carries the build identity reported to ACP agents and in
// the User-Agent of HTTP responses.
package version

// Version is the release version. Kept in one place so the CLI, the ACP
// handshake, and the HTTP layer cannot drift apart. It is a var so the release
// build can override it with -ldflags -X.
var Version = "0.1.0"

// Name is the client name sent to agents during initialize.
const Name = "acp2api"
