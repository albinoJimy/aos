module github.com/aos-ref/substrate/contract

go 1.24

require (
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

replace github.com/aos-ref/substrate/eventstore => ../eventstore
replace github.com/aos-ref/substrate/otel-genai => ../otel-genai
