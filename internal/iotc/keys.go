package iotc

// LicensePublicKeyPEM is the shipped 1024-bit license verification key.
// Derived from the shipped binary as SubjectPublicKeyInfo DER.
// The SDK uses it for the raw RSA public operation on license envelopes:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/embedded-constants.md#licence-verification-public-key
const LicensePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDQXw9rYBtx25rSHy3kNotbSoys
Cb+DqSieB7PhXgJENqOqa2Z+nlcRbgQ+lnptlVYelWPR5phmkW2OSWwoQByvXVkh
Nuiz5CAMNV+6WCYF84DQNstarDLwt7L6YhS8fTHt5ODv1uwArchCJp67+cS+gRK4
95wiu9ejOoqiDmQGYwIDAQAB
-----END PUBLIC KEY-----
`

// MasterPublicKeyPEM is the separate shipped 2048-bit master authorization key.
// Derived from the shipped binary as SubjectPublicKeyInfo DER.
// It encrypts the record-form master query; it does not verify licenses:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/embedded-constants.md#master-query-public-key
const MasterPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAn8roKGV4ukAPhiOD+Asz
eCXWjBIycEIJplBpiDcNRzfoJLXwLXCYSYsH812HaD5NnzpH7jPh/FAORNXoBkGJ
dnje299ddL7CXU1bCasSAes6FXI1XlLsBOzEW2IdM9DCyhWBx9EUOCgYHA6A+KmG
msfVOXF5eNbT6Nk7fSjGrpRGj6wDuyyhrAPuLw+yMCsyoOVMlNpziBQJqcRO5xrP
3/xHsT9f5ww/udb4+fAIlrGwv4zAkg+D2mm353G9MFoQhvd+odoa8CVrGvsetXul
pfbMkBfvgKTj7UJRb78PJQJj9WmixJc7KDKgqJNGZ1tkMwWdFfYcZH3I3kmlu6mt
hwIDAQAB
-----END PUBLIC KEY-----`
