package workshop

// signingPublicKey is the Open Workshop's index signing key (base64
// ed25519). Its private half is the repository secret WORKSHOP_SIGNING_KEY
// and nowhere else; an index without a valid signature is refused. To
// replace it: `go run ./cmd/workshop keygen`, set the new secret, run the
// workshop workflow (refresh) so the index is signed with it, then put the
// new public key here.
const signingPublicKey = "GVSJqtKqZ1ifxuUiuo2tXzsKUVRjYyRQOsilg5NLc6s="
