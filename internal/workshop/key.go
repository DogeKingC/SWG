package workshop

// signingPublicKey is the Open Workshop's index signing key (base64
// ed25519). Empty until the maintainers set one up: run
// `go run ./cmd/workshop keygen`, put the public key here and the private
// key in the repository secret WORKSHOP_SIGNING_KEY.
const signingPublicKey = ""
