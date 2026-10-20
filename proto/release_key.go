package proto

// ReleasePublicKey is the Ed25519 key (base64) every agent build trusts for
// updates and the server checks uploads against (key id 351189a8615491fb).
// The private half stays offline: ~/.config/remotectl/release.key on the
// release Mac and the RELEASE_PRIVATE_KEY GitHub secret.
var ReleasePublicKey = "oe2JxYvWjLS4XF6i0AmzkmzzTJ7BbtkALKT3nlJhO+A="
