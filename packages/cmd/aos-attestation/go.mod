module github.com/aos-ref/cmd/aos-attestation

go 1.25

toolchain go1.25.12

require (
	github.com/aos-ref/platform/attestation v0.0.0
	github.com/fxamacker/cbor/v2 v2.9.0
)

require github.com/x448/float16 v0.8.4 // indirect

replace github.com/aos-ref/platform/attestation => ../../platform/attestation
